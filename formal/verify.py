#!/usr/bin/env python3
"""Run the scheduler verification suite and retain every phase's evidence."""

import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent.parent
SMOKE_GROUPS = (
    "TestFormalBatchContractSmoke", "TestTaskLifecycleRegressionsSmoke",
    "TestTaskTagConcurrencySmoke", "TestTaskAdmissionSmoke", "TestTaskSlotAdmissionSmoke",
    "TestReadyTaskTransitionsSmoke", "TestReadyTaskPrefetchSmoke", "TestReadyTaskMigrationSmoke",
    "TestTaskAdmissionMigrationSmoke", "TestTaskLifecycleMigrationSmoke",
    "TestTaskTagConcurrencyMigrationSmoke", "TestBatchedTaskLeaseRenewalSmoke",
    "TestReadyTaskExpiredLeaseBypassesBulkSmoke", "TestReadyTaskSupplyDecisionsSmoke",
)
PHASES = (
    ("mapping", [sys.executable, "formal/audit.py"], 30),
    ("models", [sys.executable, "formal/check.py", "--timeout", "240"], 900),
    ("proofs", [sys.executable, "formal/prove.py"], 900),
    ("go", ["go", "test", "-race", "-tags=formal", "./pkg/taskcore/worker",
            "./pkg/taskcore/dtmtest", "./pkg/taskcore/store", "./pkg/taskcore/ctrl",
            "-count=1", "-json", "-timeout=180s"], 300),
    ("postgres", ["go", "test", "-tags=smoke", "./pkg/taskcore/e2e", "-run",
                  "^(" + "|".join(SMOKE_GROUPS) + ")$", "-count=1", "-json", "-timeout=300s"], 360),
)


def run(name, command, timeout, artifacts):
    log = artifacts / f"{name}.log"
    started = time.monotonic()
    print(f"Checking {name}; log: {log}", flush=True)
    timed_out = False
    with log.open("w") as output:
        with subprocess.Popen(command, cwd=ROOT, stdout=output, stderr=subprocess.STDOUT,
                              start_new_session=True) as process:
            try:
                code = process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                code, timed_out = None, True
    output = log.read_text()
    tests = []
    for line in output.splitlines():
        if line.startswith("{"):
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("Action") == "pass" and event.get("Test"):
                tests.append(event["Test"])
    passed = code == 0
    if name == "go":
        passed = passed and "TestFormalEngineConformance" in tests
    if name == "postgres":
        passed = passed and all(group in tests for group in SMOKE_GROUPS)
    result = {
        "phase": name, "passed": bool(passed), "exit_code": code, "timed_out": timed_out,
        "seconds": round(time.monotonic()-started, 3), "command": command, "log": str(log),
        "reports": re.findall(r"^Report: (.+)$", output, re.M),
        "passed_tests": tests,
    }
    print(f"{'PASS' if passed else 'FAIL'} {name}: {result['seconds']}s", flush=True)
    if not passed:
        print("\n".join(output.splitlines()[-45:]), file=sys.stderr)
    return result


def main():
    base = ROOT / ".anclax" / "formal"
    base.mkdir(parents=True, exist_ok=True)
    artifacts = Path(tempfile.mkdtemp(prefix="verification-", dir=base))
    print(f"Artifacts: {artifacts}", flush=True)
    results = []
    for name, command, timeout in PHASES:
        result = run(name, command, timeout, artifacts)
        results.append(result)
        if not result["passed"]:
            break
    report = {
        "go_version": subprocess.run(["go", "version"], capture_output=True, text=True, check=True).stdout.strip(),
        "review_sha256": hashlib.sha256((ROOT/"formal/implementation.json").read_bytes()).hexdigest(),
        "phases": results,
        "passed": len(results) == len(PHASES) and all(r["passed"] for r in results),
    }
    (artifacts / "summary.json").write_text(json.dumps(report, indent=2)+"\n")
    print(f"Report: {artifacts / 'summary.json'}", flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, subprocess.SubprocessError, ValueError) as error:
        print(f"scheduler verification failed: {error}", file=sys.stderr)
        sys.exit(1)
