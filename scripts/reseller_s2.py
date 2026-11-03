#!/usr/bin/env python3
"""Verify S2 security contracts using the shared disposable S1 database matrix."""
import argparse
import json
from pathlib import Path

from reseller_s1 import run_matrix


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--postgres-image", default="postgres:9.6")
    parser.add_argument("--database", choices=("all", "sqlite", "mysql", "postgres"), default="all")
    args = parser.parse_args()
    results = run_matrix(args.baseline.resolve(), args.output.resolve(), args.postgres_image, args.database)
    for result in results:
        events = [json.loads(line) for line in Path(result["log"]).read_text().splitlines() if line.startswith("{")]
        passed = sum(event.get("Test") == "TestResellerS1/S2_security" and event.get("Action") == "pass" for event in events)
        if passed != 1:
            raise RuntimeError("S2 security contract did not run successfully: " + result["log"])
        result["s2_security_passed"] = passed
    (args.output / "security-summary.json").write_text(json.dumps(results, indent=2) + "\n")


if __name__ == "__main__":
    main()
