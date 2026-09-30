#!/usr/bin/env python3
"""Check parameterized scheduler proofs and reject a broken reservation rule."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
VERSION = "1.5.0"
INSTALLER_SHA256 = "ebb7a3f271bdb564f74cb0a2767ef7b9ff7045621a9be7c50d363a03c2e6f08a"
INSTALLER_URL = (
    "https://github.com/tlaplus/tlapm/releases/download/202210041448/"
    "tlaps-1.5.0-x86_64-linux-gnu-inst.bin"
)
MODULES = ("SchedulerProofs", "LeaseProofs", "AdmissionProofs", "QuotaProofs", "ProgressProofs", "PolicyProofs")
MUTATIONS = {
    "MissingReservationBound": ("SchedulerProofs", "active+n <= Capacity", "active <= Capacity"),
    "MissingLeaseFence": (
        "LeaseProofs", "/\\ held = 0 /\\ owner = w /\\ epoch = v\n", "/\\ held = 0 /\\ owner = w\n",
    ),
    "MissingOverflowSerialization": (
        "QuotaProofs", "retired = 0 /\\ ordinary+private+guarded < limit", "ordinary+private+guarded < limit",
    ),
}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def prover():
    cache = ROOT / ".anclax" / "formal-tools"
    install = cache / f"tlaps-{VERSION}"
    override = os.environ.get("TLAPM")
    executable = Path(override).expanduser().resolve() if override else install / "bin" / "tlapm"
    if override and not executable.is_file():
        raise RuntimeError(f"TLAPM does not exist: {executable}")
    if not executable.is_file():
        if platform.system() != "Linux" or platform.machine() != "x86_64":
            raise RuntimeError("Automatic TLAPS installation supports Linux x86_64; set TLAPM to a TLAPS 1.5.0 executable")
        cache.mkdir(parents=True, exist_ok=True)
        installer = cache / f"tlaps-{VERSION}-inst.bin"
        if not installer.exists():
            print(f"Downloading TLAPS {VERSION} (about 145 MiB) from {INSTALLER_URL}", flush=True)
            with tempfile.NamedTemporaryFile(dir=cache, delete=False) as tmp:
                pending = Path(tmp.name)
            try:
                request = urllib.request.Request(INSTALLER_URL, headers={"User-Agent": "anclax-formal"})
                with urllib.request.urlopen(request, timeout=60) as source, pending.open("wb") as dest:
                    shutil.copyfileobj(source, dest)
                if digest(pending) != INSTALLER_SHA256:
                    raise RuntimeError("TLAPS installer checksum does not match the pinned release")
                pending.replace(installer)
            finally:
                pending.unlink(missing_ok=True)
        if digest(installer) != INSTALLER_SHA256:
            raise RuntimeError(f"TLAPS installer checksum does not match the pinned release: {installer}")
        installer.chmod(0o700)
        log = cache / f"tlaps-{VERSION}-install.log"
        print(f"Installing TLAPS in {install}; log: {log}", flush=True)
        with log.open("w") as output:
            result = subprocess.run(
                [str(installer), "-d", str(install)], cwd=cache,
                stdout=output, stderr=subprocess.STDOUT, timeout=240, check=False,
            )
        if result.returncode != 0 or not executable.is_file():
            raise RuntimeError(f"TLAPS installation failed; see {log}")
    version = subprocess.run(
        [str(executable), "--version"], capture_output=True, text=True, timeout=10, check=True,
    ).stdout.strip()
    if version != VERSION:
        raise RuntimeError(f"Expected TLAPS {VERSION}, got {version!r}: {executable}")
    return executable


def check(executable, artifacts, timeout, name):
    negative = name in MUTATIONS
    module = MUTATIONS[name][0] if negative else name
    case_dir = artifacts / name
    case_dir.mkdir()
    source = (ROOT / "formal" / "proofs" / f"{module}.tla").read_text()
    if re.search(r"\bOMITTED\b", source):
        raise RuntimeError("The proof module must not contain omitted proofs")
    if negative:
        _, original, replacement = MUTATIONS[name]
        if source.count(original) != 1:
            raise RuntimeError(f"{name} no longer matches exactly one guard")
        source = source.replace(original, replacement)
    model = case_dir / f"{module}.tla"
    model.write_text(source)
    command = [str(executable), "--threads", "1", "--nofp", model.name]
    log = case_dir / "tlaps.log"
    started = time.monotonic()
    timed_out = False
    print(f"Checking {name}...", flush=True)
    with log.open("w") as output:
        with subprocess.Popen(
            command, cwd=case_dir, stdout=output, stderr=subprocess.STDOUT,
            start_new_session=True,
        ) as process:
            try:
                returncode = process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                # Stop the prover's solver processes as well as its parent.
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                timed_out, returncode = True, None
    text = log.read_text()
    complete = re.findall(r"\[INFO\]: All (\d+) obligations? proved\.", text)
    failed = re.findall(r"\[ERROR\]: (\d+)/(\d+) obligations? failed\.", text)
    proved_count = int(complete[-1]) if complete else 0
    if negative:
        # A parse error or timeout is not evidence that the proof rejected the
        # broken rule. Require failure of the actual inductive-invariant goal.
        passed = (
            returncode == 3 and len(failed) == 1 and int(failed[0][0]) == 1
            and "PROVE  Inv /\\ [Next]_vars => Inv'" in text
        )
    else:
        passed = returncode == 0 and proved_count > 0 and "[ERROR]" not in text
        passed = passed and not re.search(r"\b(omitted|suppressed)\b", text, re.IGNORECASE)
    result = {
        "case": name, "module": module, "kind": "mutation" if negative else "proof",
        "passed": bool(passed), "exit_code": returncode, "timed_out": timed_out,
        "proved_obligations": proved_count if not negative else None,
        "failed_obligations": int(failed[0][0]) if failed else 0,
        "seconds": round(time.monotonic() - started, 3),
        "model_sha256": digest(model), "command": command, "log": str(log),
    }
    detail = "inductive proof rejected as expected" if negative else f"{proved_count} obligations proved"
    print(f"{'PASS' if passed else 'FAIL'} {name}: {detail} ({result['seconds']}s)", flush=True)
    if not passed:
        print("\n".join(text.splitlines()[-80:]), file=sys.stderr)
        if timed_out:
            print(f"TLAPS exceeded the {timeout}s timeout", file=sys.stderr)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--module", choices=MODULES, help="Check only this proof and its mutations")
    parser.add_argument("--timeout", type=int, default=120, help="Seconds per TLAPS invocation")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    executable = prover()
    base = ROOT / ".anclax" / "formal"
    base.mkdir(parents=True, exist_ok=True)
    artifacts = Path(tempfile.mkdtemp(prefix="proofs-", dir=base))
    print(f"Artifacts: {artifacts}", flush=True)
    selected = (args.module,) if args.module else MODULES
    results = [check(executable, artifacts, args.timeout, module) for module in selected]
    if all(result["passed"] for result in results):
        results.extend(
            check(executable, artifacts, args.timeout, name)
            for name, (module, _, _) in MUTATIONS.items() if module in selected
        )
    report = {
        "tlaps_version": VERSION, "executable": str(executable),
        "executable_sha256": digest(executable),
        "pinned_installer_sha256": INSTALLER_SHA256, "pinned_installer_url": INSTALLER_URL,
        "results": results,
    }
    (artifacts / "summary.json").write_text(json.dumps(report, indent=2) + "\n")
    print(f"Report: {artifacts / 'summary.json'}", flush=True)
    return 0 if all(result["passed"] for result in results) else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"formal proof failed: {error}", file=sys.stderr)
        sys.exit(1)
