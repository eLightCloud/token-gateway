#!/usr/bin/env python3
"""Isolated S0 database baseline and loopback-only upstream/mail fixtures."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import smtplib
import socketserver
import sqlite3
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid
from contextlib import contextmanager
from http.client import IncompleteRead
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


ROOT = Path(__file__).resolve().parents[1]
BASELINE_COMMIT = "21f56f1d7f5365e531d6103d6b6b407e0e0d4dca"


def command(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs)


def baseline_source():
    """Only the frozen production Go tree may generate an upgrade baseline."""
    paths = ["*.go", "go.mod", "go.sum", "**/go.mod", "**/go.sum", ":(exclude)**/*_test.go", ":(exclude)docs/**"]
    changed = command(["git", "diff", "--name-only", BASELINE_COMMIT, "--", *paths], cwd=ROOT).stdout.splitlines()
    untracked = command(["git", "ls-files", "--others", "--exclude-standard", "--", *paths], cwd=ROOT).stdout.splitlines()
    if changed or untracked:
        raise RuntimeError("upgrade baseline requires frozen production sources; changed/untracked: " + ", ".join(changed + untracked))
    assets = ["scripts/reseller_s0.py", "model/reseller_s0_baseline_test.go", "model/testdata/reseller_s0.json"]
    return {"source_commit": BASELINE_COMMIT,
            "runner_commit": command(["git", "rev-parse", "HEAD"], cwd=ROOT).stdout.strip(),
            "asset_sha256": {path: hashlib.sha256((ROOT / path).read_bytes()).hexdigest() for path in assets}}


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.do_GET()

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        code = 200
        content_type = "application/json"
        if path == "/failure":
            code, data = 503, {"error": {"message": "synthetic upstream unavailable"}}
        elif path in ("/tasks/pending", "/tasks/success", "/tasks/failure"):
            status = path.rsplit("/", 1)[1]
            data = {"id": "s0-task", "status": status, "usage": {"tokens": 60 if status == "success" else 0}}
        elif path in ("/stream", "/stream-abort"):
            content_type = "text/event-stream"
            data = 'data: {"choices":[{"delta":{"content":"s0"}}]}\n\n'
            if path == "/stream":
                data += 'data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}\n\ndata: [DONE]\n\n'
        elif path in ("/success", "/v1/chat/completions"):
            data = {"id": "s0-completion", "object": "chat.completion", "model": "s0-model", "choices": [{"index": 0, "message": {"role": "assistant", "content": "s0"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
        else:
            code, data = 404, {"error": {"message": "unknown S0 scenario"}}
        body = (data if isinstance(data, str) else json.dumps(data)).encode()
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body) + (20 if path == "/stream-abort" else 0)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.wfile.flush()
        self.close_connection = True


class MailHandler(socketserver.StreamRequestHandler):
    def handle(self):
        self.wfile.write(b"220 S0 loopback mail sink\r\n")
        while line := self.rfile.readline():
            verb = line.split(maxsplit=1)[0].upper()
            if verb in (b"EHLO", b"HELO", b"MAIL", b"RCPT", b"RSET"):
                self.wfile.write(b"250 OK\r\n")
            elif verb == b"DATA":
                self.wfile.write(b"354 End with dot\r\n")
                parts = []
                while (part := self.rfile.readline()) not in (b".\r\n", b""):
                    parts.append(part)
                self.server.inbox.append(b"".join(parts))
                self.wfile.write(b"250 Stored locally\r\n")
            elif verb == b"QUIT":
                self.wfile.write(b"221 Bye\r\n")
                return
            else:
                self.wfile.write(b"502 Unsupported\r\n")


class MailSink(socketserver.ThreadingTCPServer):
    daemon_threads = True

    def __init__(self, address):
        self.inbox = []
        super().__init__(address, MailHandler)


@contextmanager
def fixtures(http_port=0, smtp_port=0):
    with ThreadingHTTPServer(("127.0.0.1", http_port), Upstream) as http, MailSink(("127.0.0.1", smtp_port)) as smtp:
        threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in (http, smtp)]
        for thread in threads:
            thread.start()
        try:
            yield http, smtp
        finally:
            http.shutdown()
            smtp.shutdown()
            for thread in threads:
                thread.join()


def check_harness():
    with fixtures() as (http, smtp):
        base = f"http://127.0.0.1:{http.server_port}"
        with urllib.request.urlopen(base + "/success", timeout=5) as response:
            assert json.load(response)["usage"]["total_tokens"] == 15
        with urllib.request.urlopen(base + "/stream", timeout=5) as response:
            assert response.read().endswith(b"data: [DONE]\n\n")
        try:
            with urllib.request.urlopen(base + "/stream-abort", timeout=5) as response:
                response.read()
        except IncompleteRead:
            pass
        else:
            raise AssertionError("aborted stream was not observable")
        for status in ("pending", "success", "failure"):
            with urllib.request.urlopen(base + "/tasks/" + status, timeout=5) as response:
                assert json.load(response)["status"] == status
        try:
            urllib.request.urlopen(base + "/failure", timeout=5)
        except urllib.error.HTTPError as error:
            assert error.code == 503
        else:
            raise AssertionError("failure fixture did not fail")
        with smtplib.SMTP("127.0.0.1", smtp.server_address[1], timeout=5) as client:
            client.sendmail("sender@example.invalid", ["customer@example.invalid"], "Subject: S0 activation fixture\r\n\r\nhttp://127.0.0.1/activate?token=synthetic-only\r\n")
        assert len(smtp.inbox) == 1 and b"token=synthetic-only" in smtp.inbox[0]
    return {"upstream": ["success_usage", "stream_done", "stream_abort", "pending_task", "successful_task", "failed_task", "http_503"], "mail": "loopback SMTP captured synthetic activation URL", "scope": "fixture transport only; not gateway or new activation integration"}


def database_matrix(output, postgres_image, selected, full_startup):
    results = []
    for kind, image in (("sqlite", None), ("mysql", "mysql:5.7.44"), ("postgres", postgres_image)):
        if selected != "all" and kind != selected:
            continue
        name = f"token-gateway-s0-{kind}-{uuid.uuid4().hex[:8]}"
        owned = False
        environment = dict(os.environ, S0_MATRIX_DIALECT=kind)
        environment.pop("S0_VERIFY_RESTORE", None)
        environment["S0_FULL_STARTUP"] = "1" if full_startup else "0"
        if full_startup and kind == "sqlite":
            environment["S0_SQLITE_SNAPSHOT"] = str((output / (uuid.uuid4().hex + "-baseline.sqlite")).resolve())
        result = {"database": kind, "image": image, "full_startup": full_startup, "status": "running"}
        results.append(result)
        primary_error = None
        try:
            if full_startup:
                result.update(baseline_source())
            digest = None
            if image:
                metadata = json.loads(command(["docker", "image", "inspect", image]).stdout)[0]
                digest = metadata["Id"]
                result["image_id"] = digest
                port = 3306 if kind == "mysql" else 5432
                options = ["-e", "MYSQL_ROOT_PASSWORD=s0-synthetic", "-e", "MYSQL_DATABASE=s0_baseline"] if kind == "mysql" else ["-e", "POSTGRES_USER=s0", "-e", "POSTGRES_PASSWORD=s0-synthetic", "-e", "POSTGRES_DB=s0_baseline"]
                command(["docker", "run", "--pull=never", "--rm", "-d", "--name", name, "--label", "token-gateway.test=s0", "--platform", "linux/" + metadata["Architecture"], "-p", f"127.0.0.1::{port}", *options, image])
                owned = True
                ready = ["mysql", "--protocol=TCP", "-h127.0.0.1", "-uroot", "-ps0-synthetic", "-N", "-e", "SELECT 1"] if kind == "mysql" else ["pg_isready", "-h127.0.0.1", "-U", "s0", "-d", "s0_baseline"]
                deadline = time.monotonic() + 180
                while True:
                    attempt = subprocess.run(["docker", "exec", name, *ready], capture_output=True)
                    if attempt.returncode == 0:
                        break
                    if time.monotonic() > deadline:
                        raise RuntimeError(f"{kind} disposable server readiness timeout")
                    time.sleep(1)
                binding = command(["docker", "port", name, f"{port}/tcp"]).stdout.strip()
                host_port = int(binding.rsplit(":", 1)[1])
                environment["S0_MATRIX_DSN"] = f"root:s0-synthetic@tcp(127.0.0.1:{host_port})/s0_baseline?charset=utf8mb4&parseTime=true&loc=UTC" if kind == "mysql" else f"host=127.0.0.1 port={host_port} user=s0 password=s0-synthetic dbname=s0_baseline sslmode=disable"
            log = output / f"matrix-{kind}.jsonl"
            result["log"] = str(log)
            with log.open("w") as stream:
                run = subprocess.run(["go", "test", "-p", "2", "-count=1", "-json", "./model", "-run", "^TestResellerS0DatabaseBaseline$"], cwd=ROOT, env=environment, stdout=stream, stderr=subprocess.STDOUT, timeout=900)
            events = []
            for line in log.read_text().splitlines():
                if line.startswith("{"):
                    events.append(json.loads(line))
            passes = [event for event in events if event.get("Action") == "pass" and event.get("Test") == "TestResellerS0DatabaseBaseline"]
            versions = [event["Output"].strip() for event in events if "S0 database=" in event.get("Output", "")]
            result.update(image_id=digest, exit_code=run.returncode, passes=len(passes), versions=versions, log=str(log))
            if run.returncode != 0 or len(passes) != 1:
                raise RuntimeError(f"{kind} baseline failed; inspect {log}")
            if full_startup and image:
                dump = ["mysqldump", "-uroot", "-ps0-synthetic", "--single-transaction", "--skip-comments", "s0_baseline"] if kind == "mysql" else ["pg_dump", "-U", "s0", "-d", "s0_baseline", "--no-owner", "--no-acl"]
                snapshot = output / (kind + "-baseline.sql")
                snapshot.write_text(command(["docker", "exec", name, *dump]).stdout)
                result["snapshot"] = str(snapshot)
            if full_startup:
                key_column = '"key"' if kind == "postgres" else "`key`"
                query = f"""SELECT quota,
                    (SELECT amount_used FROM user_subscriptions WHERE id=1),
                    (SELECT quota FROM logs WHERE user_id=201 LIMIT 1),
                    (SELECT COUNT(*) FROM tokens WHERE id=201 AND user_id=201 AND status=1 AND remain_quota=50 AND used_quota=0 AND name='s0-synthetic' AND expired_time=-1 AND unlimited_quota=false AND {key_column}='s0synthetic0000000000000000000001'),
                    (SELECT COUNT(*) FROM organizations WHERE id=1 AND name='s0-existing-org' AND status=1),
                    (SELECT COUNT(*) FROM organization_members WHERE id=1 AND organization_id=1 AND user_id=201 AND role='member' AND joined_at=100 AND left_at=0)
                    FROM users WHERE id=201"""
                if kind == "sqlite":
                    snapshot = Path(environment["S0_SQLITE_SNAPSHOT"])
                    with sqlite3.connect(snapshot) as connection:
                        restored = connection.execute(query).fetchone()
                elif kind == "mysql":
                    command(["docker", "exec", name, "mysql", "-uroot", "-ps0-synthetic", "-e", "CREATE DATABASE s0_restore CHARACTER SET utf8mb4"])
                    command(["docker", "exec", "-i", name, "mysql", "-uroot", "-ps0-synthetic", "s0_restore"], input=snapshot.read_text())
                    values = command(["docker", "exec", name, "mysql", "-uroot", "-ps0-synthetic", "-N", "s0_restore", "-e", query]).stdout.strip()
                    restored = tuple(map(int, values.split()))
                else:
                    command(["docker", "exec", name, "createdb", "-U", "s0", "s0_restore"])
                    command(["docker", "exec", "-i", name, "psql", "-v", "ON_ERROR_STOP=1", "-U", "s0", "-d", "s0_restore"], input=snapshot.read_text())
                    values = command(["docker", "exec", name, "psql", "-U", "s0", "-d", "s0_restore", "-At", "-F", ",", "-c", query]).stdout.strip()
                    restored = tuple(map(int, values.split(",")))
                if restored != (1000, 500, 54, 1, 1, 1):
                    raise RuntimeError(f"{kind} restored snapshot changed wallet/subscription/log/key/organization/member facts")
                # Hash the original artifact before disposable restore probes.
                result.update(snapshot=str(snapshot), snapshot_sha256=hashlib.sha256(snapshot.read_bytes()).hexdigest())
                restore_environment = dict(environment, S0_VERIFY_RESTORE="1")
                if kind != "sqlite":
                    restore_environment["S0_MATRIX_DSN"] = environment["S0_MATRIX_DSN"].replace("s0_baseline", "s0_restore")
                else:
                    # SQLite probes also use a copy, preserving the frozen artifact.
                    probe = output / "sqlite-restore-probe.sqlite"
                    probe.write_bytes(snapshot.read_bytes())
                    restore_environment["S0_SQLITE_SNAPSHOT"] = str(probe.resolve())
                restore_log = output / f"restore-{kind}.jsonl"
                result["restore_log"] = str(restore_log)
                with restore_log.open("w") as stream:
                    restored_run = subprocess.run(["go", "test", "-p", "2", "-count=1", "-json", "./model", "-run", "^TestResellerS0DatabaseBaseline$"], cwd=ROOT, env=restore_environment, stdout=stream, stderr=subprocess.STDOUT, timeout=900)
                restore_events = [json.loads(line) for line in restore_log.read_text().splitlines() if line.startswith("{")]
                restore_passes = sum(event.get("Action") == "pass" and event.get("Test") == "TestResellerS0DatabaseBaseline" for event in restore_events)
                if restored_run.returncode != 0 or restore_passes != 1:
                    raise RuntimeError(f"{kind} restored auto-increment validation failed; inspect {restore_log}")
                result.update(restore_verified=True, restore_auto_increment_verified=True, restore_passes=restore_passes)
            result["status"] = "passed"
        except BaseException as error:
            primary_error = error
            result.update(status="failed", error_type=type(error).__name__, error=str(error))
            if isinstance(error, subprocess.CalledProcessError):
                result["stderr"] = error.stderr
            raise
        finally:
            cleanup_error = None
            if owned:
                try:
                    command(["docker", "rm", "-f", name])
                    result["cleanup"] = "removed"
                except subprocess.CalledProcessError as error:
                    if "No such container" in (error.stderr or ""):
                        result["cleanup"] = "already_absent"
                    else:
                        cleanup_error = error
                        result.update(status="failed", cleanup_error=str(error), cleanup_stderr=error.stderr)
                except Exception as error:
                    cleanup_error = error
                    result.update(status="failed", cleanup_error=str(error))
            (output / "matrix-summary.json").write_text(json.dumps(results, indent=2) + "\n")
            if cleanup_error is not None and primary_error is None:
                raise cleanup_error
        print(json.dumps(result), flush=True)
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("harness", "serve", "matrix"))
    parser.add_argument("--output", type=Path)
    parser.add_argument("--http-port", type=int, default=0)
    parser.add_argument("--smtp-port", type=int, default=0)
    parser.add_argument("--postgres-image", default="postgres:9.6")
    parser.add_argument("--database", choices=("all", "sqlite", "mysql", "postgres"), default="all")
    parser.add_argument("--full-startup", action="store_true")
    args = parser.parse_args()
    if args.mode == "serve":
        with fixtures(args.http_port, args.smtp_port) as (http, smtp):
            print(json.dumps({"http": f"http://127.0.0.1:{http.server_port}", "smtp_port": smtp.server_address[1], "scope": "synthetic loopback fixtures; no SMTP authentication or TLS"}), flush=True)
            try:
                threading.Event().wait()
            except KeyboardInterrupt:
                pass
        return
    if args.output is None:
        parser.error("--output is required for evidence")
    args.output.mkdir(parents=True, exist_ok=True)
    if args.mode == "matrix" and any(args.output.iterdir()):
        parser.error("matrix --output must be empty; preserve existing baseline artifacts and hashes")
    if args.mode == "harness":
        result = check_harness()
        (args.output / "harness-summary.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result))
    else:
        database_matrix(args.output, args.postgres_image, args.database, args.full_startup)


if __name__ == "__main__":
    main()
