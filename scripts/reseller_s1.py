#!/usr/bin/env python3
"""Run S1 against disposable databases, including frozen S0 upgrade snapshots."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time
import uuid

from reseller_s0 import ROOT, BASELINE_COMMIT, command


def run_matrix(baseline, output, postgres_image, selected):
    if output.exists() and any(output.iterdir()):
        raise RuntimeError("output directory must be empty; preserve existing evidence")
    manifest = json.loads((baseline / "matrix-summary.json").read_text())
    snapshots = {}
    for item in manifest:
        if item["status"] != "passed" or item.get("source_commit") != BASELINE_COMMIT or not item.get("restore_verified") or not item.get("restore_auto_increment_verified"):
            raise RuntimeError("baseline must be a verified frozen S0 snapshot with restored auto-increment checks")
        source = Path(item["snapshot"])
        if hashlib.sha256(source.read_bytes()).hexdigest() != item["snapshot_sha256"]:
            raise RuntimeError("baseline checksum mismatch")
        snapshots[item["database"]] = source
    required = {"sqlite", "mysql", "postgres"} if selected == "all" else {selected}
    if not required.issubset(snapshots):
        raise RuntimeError("selected baseline snapshots are required")
    output.mkdir(parents=True, exist_ok=True)
    results = []
    for kind, image in (("sqlite", None), ("mysql", "mysql:5.7.44"), ("postgres", postgres_image)):
        if selected != "all" and kind != selected:
            continue
        name = "token-gateway-s1-" + kind + "-" + uuid.uuid4().hex[:8]
        owned = False
        environment = dict(os.environ, S1_MATRIX_DIALECT=kind)
        try:
            if image:
                metadata = json.loads(command(["docker", "image", "inspect", image]).stdout)[0]
                port = 3306 if kind == "mysql" else 5432
                options = ["-e", "MYSQL_ROOT_PASSWORD=s1-synthetic", "-e", "MYSQL_DATABASE=s1_test"] if kind == "mysql" else ["-e", "POSTGRES_USER=s1", "-e", "POSTGRES_PASSWORD=s1-synthetic", "-e", "POSTGRES_DB=s1_test"]
                command(["docker", "run", "--pull=never", "--rm", "-d", "--name", name, "--label", "token-gateway.test=s1", "--platform", "linux/"+metadata["Architecture"], "-p", f"127.0.0.1::{port}", *options, image])
                owned = True
                ready = ["mysql", "--protocol=TCP", "-h127.0.0.1", "-uroot", "-ps1-synthetic", "-N", "-e", "SELECT 1"] if kind == "mysql" else ["pg_isready", "-h127.0.0.1", "-U", "s1", "-d", "s1_test"]
                deadline = time.monotonic()+180
                while subprocess.run(["docker", "exec", name, *ready], capture_output=True).returncode:
                    if time.monotonic() > deadline:
                        raise RuntimeError("disposable database did not become ready")
                    time.sleep(.5)
                if kind == "mysql":
                    command(["docker", "exec", name, "mysql", "-uroot", "-ps1-synthetic", "-e", "ALTER DATABASE s1_test CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"])
                hostport = command(["docker", "port", name, str(port)]).stdout.strip().rsplit(":", 1)[1]
                environment["S1_MATRIX_DSN"] = f"root:s1-synthetic@tcp(127.0.0.1:{hostport})/s1_test?charset=utf8mb4&parseTime=True&loc=Local" if kind == "mysql" else f"host=127.0.0.1 port={hostport} user=s1 password=s1-synthetic dbname=s1_test sslmode=disable"
            for upgrade in (False, True):
                environment["S1_UPGRADE"] = "1" if upgrade else "0"
                if kind == "sqlite":
                    environment["S1_MATRIX_SQLITE"] = str(output / f"sqlite-{'upgrade' if upgrade else 'fresh'}.db")
                if upgrade:
                    if kind == "sqlite":
                        environment["S1_BASELINE_SQLITE"] = str(snapshots[kind])
                    elif kind == "mysql":
                        command(["docker", "exec", name, "mysql", "-uroot", "-ps1-synthetic", "-e", "DROP DATABASE s1_test; CREATE DATABASE s1_test CHARACTER SET utf8mb4"])
                        command(["docker", "exec", "-i", name, "mysql", "-uroot", "-ps1-synthetic", "s1_test"], input=snapshots[kind].read_text())
                    else:
                        command(["docker", "exec", name, "dropdb", "-U", "s1", "s1_test"])
                        command(["docker", "exec", name, "createdb", "-U", "s1", "s1_test"])
                        command(["docker", "exec", "-i", name, "psql", "-v", "ON_ERROR_STOP=1", "-U", "s1", "-d", "s1_test"], input=snapshots[kind].read_text())
                log = output / f"{kind}-{'upgrade' if upgrade else 'fresh'}.jsonl"
                with log.open("w") as stream:
                    run = subprocess.run(["go", "test", "-p", "2", "-count=1", "-json", "./model", "-run", "^TestResellerS1$"], cwd=ROOT, env=environment, stdout=stream, stderr=subprocess.STDOUT, timeout=900)
                events = [json.loads(line) for line in log.read_text().splitlines() if line.startswith("{")]
                passed = sum(e.get("Test") == "TestResellerS1" and e.get("Action") == "pass" for e in events)
                result = dict(database=kind, image=image, image_id=metadata["Id"] if image else None, source_commit=BASELINE_COMMIT, baseline_sha256=hashlib.sha256(snapshots[kind].read_bytes()).hexdigest() if upgrade else None, upgrade=upgrade, exit_code=run.returncode, passed=passed, log=str(log), versions=[e["Output"].strip() for e in events if "S1 database=" in e.get("Output", "")])
                results.append(result)
                (output / "summary.json").write_text(json.dumps(results, indent=2)+"\n")
                print(json.dumps(result), flush=True)
                if run.returncode or passed != 1:
                    raise RuntimeError("S1 matrix failed: " + str(log))
                funding_log = output / f"{kind}-{'upgrade' if upgrade else 'fresh'}-funding.jsonl"
                with funding_log.open("w") as stream:
                    funding_run = subprocess.run(["go", "test", "-p", "2", "-count=1", "-json", "./service", "-run", "^TestPersonalFundingCorrectness$"], cwd=ROOT, env=environment, stdout=stream, stderr=subprocess.STDOUT, timeout=900)
                funding_events = [json.loads(line) for line in funding_log.read_text().splitlines() if line.startswith("{")]
                funding_passed = sum(e.get("Test") == "TestPersonalFundingCorrectness" and e.get("Action") == "pass" for e in funding_events)
                result.update(funding_exit_code=funding_run.returncode, funding_passed=funding_passed, funding_log=str(funding_log), funding_versions=[e["Output"].strip() for e in funding_events if "personal funding database=" in e.get("Output", "")])
                (output / "summary.json").write_text(json.dumps(results, indent=2)+"\n")
                print(json.dumps(result), flush=True)
                if funding_run.returncode or funding_passed != 1:
                    raise RuntimeError("funding regression matrix failed: " + str(funding_log))
        finally:
            if owned:
                command(["docker", "rm", "-f", name])
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--postgres-image", default="postgres:9.6")
    parser.add_argument("--database", choices=("all", "sqlite", "mysql", "postgres"), default="all")
    args = parser.parse_args()
    run_matrix(args.baseline.resolve(), args.output.resolve(), args.postgres_image, args.database)
