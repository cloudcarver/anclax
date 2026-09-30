#!/usr/bin/env python3
"""Run the scheduler's bounded models, mutation checks, and reachability witnesses."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
TOOLS_VERSION = "1.7.4"
TOOLS_SHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"
TOOLS_URL = (
    f"https://github.com/tlaplus/tlaplus/releases/download/v{TOOLS_VERSION}/tla2tools.jar"
)
# A negative check passes only for the named invariant and TLC's invariant exit
# code. Syntax/type errors, timeouts and other checker failures must fail the run.
MODELS = {
    "tasklease": ("TaskLease", {
        "TaskLease": ("safety", None),
        "TwoTasks": ("safety", None),
        "NoFinalizeFence": ("mutation", "NoStaleFinalize"),
        "OverwriteControl": ("mutation", "ControlWins"),
        "ReleaseOtherTasks": ("mutation", "ResourcesAccounted"),
        "ReachTakeover": ("witness", "NoSameWorkerTakeover"),
        "ReachReady": ("witness", "NoReadyExecution"),
        "ReachResume": ("witness", "NoRetainedLeaseAfterResume"),
    }),
    "tagslots": ("TagSlots", {
        "MultiTag": ("safety", None),
        "Overflow": ("safety", None),
        "StaleSlot": ("mutation", "AllOrNothing"),
        "PartialRollback": ("mutation", "AllOrNothing"),
        "NoOverflowCount": ("mutation", "NoNewOversubscription"),
        "NoOverflowGuard": ("mutation", "NoNewOversubscription"),
        "NoConfigBarrier": ("mutation", "NoNewOversubscription"),
        "ReachOverflow": ("witness", "NoOverflow"),
        "ReachPartial": ("witness", "NoPartialAllocation"),
        "ReachStaleCursor": ("witness", "NoStaleCursor"),
    }),
    "workerbudget": ("WorkerBudget", {
        "WorkerBudget": ("safety", None),
        "NoBatchReservation": ("mutation", "BudgetAccounting"),
        "EarlyCapacityRelease": ("mutation", "BudgetAccounting"),
        "DuplicateFinalize": ("mutation", "BudgetAccounting"),
        "AdmissionAfterStop": ("mutation", "NoPostStopAdmission"),
        "LostFallbackReservation": ("mutation", "BudgetAccounting"),
        "ReachStrictShrink": ("witness", "NoGrandfatheredStrict"),
        "ReachControlCapacity": ("witness", "NoControlBesideFullBusiness"),
        "ReachStoppedDrain": ("witness", "NoStoppedFinalization"),
    }),
    "composed": ("Scheduler", {
        "ComposedLease": ("safety", None),
        "ComposedTakeover": ("safety", None),
        "ComposedMultiTag": ("safety", None),
        "ComposedBatch": ("safety", None),
        "ComposedSerial": ("safety", None),
        "ComposedCompletion": ("liveness", None),
        "UnboundedBatchReply": ("mutation", "ResponseContract"),
        "StaleScheduler": ("mutation", "SchedulerFence"),
        "MissingSerialCheck": ("mutation", "SerialSafety"),
        "ReachOldExecution": ("witness", "NoOldExecution"),
    }),
    "progress": ("Progress", {
        "FairCompletion": ("liveness", None),
        "NoFairness": ("liveness-negative", "EventuallyDone"),
        "StuckHandler": ("liveness-negative", "EventuallyDone"),
        "ZeroQuota": ("liveness-negative", "EventuallyDone"),
    }),
    "guarantees": ("Guarantees", {
        "PriorityStarvation": ("liveness-negative", "NormalProgress"),
        "DuplicateSideEffect": ("counterexample", "ExactlyOnce"),
    }),
}
IMPORTS = {"composed": ("tasklease/TaskLease.tla", "tagslots/TagSlots.tla")}
CASES = {
    name: (directory, module, kind, expected)
    for directory, (module, cases) in MODELS.items()
    for name, (kind, expected) in cases.items()
}
IMPLEMENTATION = (
    "sql/queries/tasks.sql",
    "sql/queries/task_batch.sql",
    "sql/migrations/0015_task_admission.up.sql",
    "sql/migrations/0016_ready_task_prefetch.up.sql",
    "pkg/taskcore/worker/lifecycle_handler.go",
    "pkg/taskcore/worker/lifecycle_policy.go",
    "pkg/taskcore/worker/finalize_retry.go",
    "pkg/taskcore/worker/engine.go",
    "pkg/taskcore/worker/batch.go",
    "pkg/taskcore/worker/runtime.go",
    "pkg/taskcore/worker/lease_manager.go",
    "pkg/taskcore/worker/prefetch.go",
    "pkg/zgen/querier/task_batch.sql.gen.go",
)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tools_jar(override):
    path = Path(override).expanduser().resolve() if override else (
        ROOT / ".anclax" / "formal-tools" / f"tla2tools-{TOOLS_VERSION}.jar"
    )
    if not path.is_file():
        if override:
            raise RuntimeError(f"TLA2TOOLS_JAR does not exist: {path}")
        path.parent.mkdir(parents=True, exist_ok=True)
        print(f"Downloading TLA+ tools {TOOLS_VERSION} from {TOOLS_URL}", flush=True)
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
            pending = Path(tmp.name)
        try:
            request = urllib.request.Request(TOOLS_URL, headers={"User-Agent": "anclax-formal"})
            with urllib.request.urlopen(request, timeout=60) as source, pending.open("wb") as dest:
                shutil.copyfileobj(source, dest)
            if digest(pending) != TOOLS_SHA256:
                raise RuntimeError("Downloaded TLA+ tools checksum does not match the pinned release")
            pending.replace(path)
        finally:
            pending.unlink(missing_ok=True)
    if digest(path) != TOOLS_SHA256:
        raise RuntimeError(f"TLA+ tools checksum does not match the pinned release: {path}")
    return path


def check(name, jar, artifacts, timeout):
    directory, module, kind, expected = CASES[name]
    model = ROOT / "formal" / directory
    case_dir = artifacts / name
    case_dir.mkdir()
    # Keep generated TLC files and the exact checked inputs with their logs.
    for source in (model / f"{module}.tla", model / f"{name}.cfg"):
        shutil.copy2(source, case_dir / source.name)
    for dependency in IMPORTS.get(directory, ()):
        source = ROOT / "formal" / dependency
        shutil.copy2(source, case_dir / source.name)
    command = [
        "java", "-XX:+UseParallelGC", "-Xmx1g", "-cp", str(jar), "tlc2.TLC",
        "-workers", "1", "-fp", "0", "-seed", "1", "-coverage", "1",
        "-config", f"{name}.cfg", f"{module}.tla",
    ]
    log = case_dir / "tlc.log"
    started = time.monotonic()
    timed_out = False
    print(f"Checking {name} ({kind})...", flush=True)
    with log.open("w") as output:
        try:
            process = subprocess.run(
                command, cwd=case_dir, stdout=output, stderr=subprocess.STDOUT,
                timeout=timeout, check=False,
            )
            returncode = process.returncode
        except subprocess.TimeoutExpired:
            timed_out, returncode = True, None
    text = log.read_text()
    if kind == "liveness-negative":
        # TLC reports a temporal counterexample without naming the property.
        # Require exactly the requested property in the copied input as well.
        properties = re.findall(r"^PROPERTY\s+(\w+)\s*$", (case_dir / f"{name}.cfg").read_text(), re.M)
        passed = returncode == 13 and "Temporal properties were violated." in text and properties == [expected]
    elif expected:
        passed = returncode == 12 and f"Invariant {expected} is violated." in text
    else:
        passed = returncode == 0 and "Model checking completed. No error has been found." in text
    stats = re.findall(
        r"([\d,]+) states generated, ([\d,]+) distinct states found, ([\d,]+) states left on queue\.",
        text,
    )
    counts = [int(value.replace(",", "")) for value in stats[-1]] if stats else None
    if not expected:
        passed = passed and counts is not None and counts[2] == 0
    result = {
        "case": name, "module": module, "kind": kind, "passed": passed,
        "expected_invariant": expected, "exit_code": returncode, "timed_out": timed_out,
        "seconds": round(time.monotonic() - started, 3),
        "generated_states": counts[0] if counts else None,
        "distinct_states": counts[1] if counts else None,
        "queued_states": counts[2] if counts else None,
        "model_sha256": digest(case_dir / f"{module}.tla"),
        "config_sha256": digest(case_dir / f"{name}.cfg"),
        "import_sha256": {
            dependency: digest(ROOT / "formal" / dependency)
            for dependency in IMPORTS.get(directory, ())
        },
        "command": command, "log": str(log),
    }
    detail = f"expected counterexample: {expected}" if expected else f"{result['distinct_states']} states"
    print(f"{'PASS' if passed else 'FAIL'} {name}: {detail} ({result['seconds']}s)", flush=True)
    if not passed:
        error_start = text.find("Error:")
        excerpt = text[error_start:] if error_start >= 0 else text
        print("\n".join(excerpt.splitlines()[:70]), file=sys.stderr)
        if timed_out:
            print(f"TLC exceeded the {timeout}s timeout", file=sys.stderr)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--case", choices=CASES, action="append", help="Run only these cases")
    selection.add_argument("--model", choices=MODELS, help="Run one model and its negative checks")
    parser.add_argument("--timeout", type=int, default=120, help="Seconds per TLC invocation")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    if shutil.which("java") is None:
        raise RuntimeError("Java is required; this runner is validated with Java 17")
    jar = tools_jar(os.environ.get("TLA2TOOLS_JAR"))
    base = ROOT / ".anclax" / "formal"
    base.mkdir(parents=True, exist_ok=True)
    artifacts = Path(tempfile.mkdtemp(prefix="scheduler-", dir=base))
    print(f"Artifacts: {artifacts}", flush=True)
    selected = dict.fromkeys(args.case or (MODELS[args.model][1] if args.model else CASES))
    results = [check(name, jar, artifacts, args.timeout) for name in selected]
    report = {
        "tools_version": TOOLS_VERSION, "tools_sha256": TOOLS_SHA256,
        "java_version": subprocess.run(
            ["java", "-version"], capture_output=True, text=True, check=True,
        ).stderr.strip(),
        # These hashes identify the reviewed implementation; they do not prove
        # that its behavior refines the specification.
        "implementation_sha256": {name: digest(ROOT / name) for name in IMPLEMENTATION},
        "results": results,
    }
    (artifacts / "summary.json").write_text(json.dumps(report, indent=2) + "\n")
    print(f"Report: {artifacts / 'summary.json'}", flush=True)
    return 0 if all(result["passed"] for result in results) else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError) as error:
        print(f"formal check failed: {error}", file=sys.stderr)
        sys.exit(1)
