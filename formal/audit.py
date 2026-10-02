#!/usr/bin/env python3
"""Reject implementation drift from the explicitly reviewed model mappings."""

import hashlib
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent.parent


def main():
    review = json.loads((ROOT / "formal" / "implementation.json").read_text())
    failures = []
    for name, expected in review["sources"].items():
        path = ROOT / name
        if not path.is_file():
            failures.append(f"missing reviewed implementation: {name}")
        elif hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            failures.append(f"implementation changed since model mapping review: {name}")
    for mapping in review["mappings"]:
        for name in mapping["sources"]:
            if name not in review["sources"]:
                failures.append(f"unversioned mapping source: {name}")
        for name in mapping["specifications"] + mapping["implementation_checks"]:
            if not (ROOT / name).is_file():
                failures.append(f"missing mapping evidence: {name}")
    if failures:
        print("\n".join(failures), file=sys.stderr)
        print("Review the affected mapping and checks before updating implementation.json; hashes do not prove refinement.", file=sys.stderr)
        return 1
    print(f"Reviewed implementation matches: {len(review['sources'])} files, {len(review['mappings'])} mappings (manual mapping, not a source-level proof)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError) as error:
        print(f"implementation audit failed: {error}", file=sys.stderr)
        sys.exit(1)
