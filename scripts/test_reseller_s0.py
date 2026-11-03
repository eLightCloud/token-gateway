"""Regression tests for S0 runner failure evidence; no Docker or database access."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import reseller_s0


class MatrixEvidenceTests(unittest.TestCase):
    def test_baseline_source_rejects_changed_production(self):
        for changed, untracked in (("model/user.go\n", ""), ("", "model/new_feature.go\n"), ("", "")):
            with self.subTest(changed=changed, untracked=untracked):
                def command(args, **kwargs):
                    value = changed if args[1] == "diff" else untracked if args[1] == "ls-files" else "runner-head\n"
                    return subprocess.CompletedProcess(args, 0, value)

                with patch.object(reseller_s0, "command", side_effect=command):
                    if changed or untracked:
                        with self.assertRaisesRegex(RuntimeError, "requires frozen production sources"):
                            reseller_s0.baseline_source()
                    else:
                        evidence = reseller_s0.baseline_source()
                        self.assertEqual(evidence["source_commit"], reseller_s0.BASELINE_COMMIT)
                        self.assertEqual(evidence["runner_commit"], "runner-head")
                        self.assertIn("model/testdata/reseller_s0.json", evidence["asset_sha256"])

    def test_existing_baseline_output_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            sentinel = Path(directory) / "matrix-summary.json"
            sentinel.write_text("frozen evidence\n")
            run = subprocess.run(["python3", str(Path(reseller_s0.__file__)), "matrix", "--output", directory], capture_output=True, text=True)
            self.assertEqual(run.returncode, 2)
            self.assertIn("must be empty", run.stderr)
            self.assertEqual(sentinel.read_text(), "frozen evidence\n")

    def test_primary_failure_and_cleanup_evidence(self):
        for fails, cleanup in ((False, "removed"), (True, "removed"),
                               (False, "absent"), (True, "absent"),
                               (False, "denied"), (True, "denied")):
            with self.subTest(fails=fails, cleanup=cleanup), tempfile.TemporaryDirectory() as directory:
                output = Path(directory)
                primary = RuntimeError("original matrix failure")
                cleanup_failure = subprocess.CalledProcessError(1, ["docker", "rm"], stderr="permission denied")

                def command(args, **kwargs):
                    if args[:3] == ["docker", "image", "inspect"]:
                        return subprocess.CompletedProcess(args, 0, '[{"Id":"synthetic","Architecture":"arm64"}]')
                    if args[:2] == ["docker", "port"]:
                        if fails:
                            raise primary
                        return subprocess.CompletedProcess(args, 0, "127.0.0.1:12345")
                    if args[:3] == ["docker", "rm", "-f"]:
                        if cleanup == "absent":
                            raise subprocess.CalledProcessError(1, args, stderr="No such container: synthetic")
                        if cleanup == "denied":
                            raise cleanup_failure
                    return subprocess.CompletedProcess(args, 0, "synthetic")

                def run(args, **kwargs):
                    if args[0] == "go":
                        kwargs["stdout"].write(json.dumps({"Action": "pass", "Test": "TestResellerS0DatabaseBaseline"}) + "\n")
                    return subprocess.CompletedProcess(args, 0)

                with patch.object(reseller_s0, "command", side_effect=command), patch.object(reseller_s0.subprocess, "run", side_effect=run):
                    if fails or cleanup == "denied":
                        with self.assertRaises(Exception) as caught:
                            reseller_s0.database_matrix(output, "synthetic", "postgres", False)
                        self.assertIs(caught.exception, primary if fails else cleanup_failure)
                    else:
                        reseller_s0.database_matrix(output, "synthetic", "postgres", False)
                evidence = json.loads((output / "matrix-summary.json").read_text())[0]
                self.assertEqual(evidence["status"], "failed" if fails or cleanup == "denied" else "passed")
                if fails:
                    self.assertEqual(evidence["error"], str(primary))
                if cleanup == "denied":
                    self.assertEqual(evidence["cleanup_stderr"], "permission denied")
                else:
                    self.assertEqual(evidence["cleanup"], "already_absent" if cleanup == "absent" else "removed")

    def test_failure_before_container_creation_is_recorded(self):
        with tempfile.TemporaryDirectory() as directory:
            primary = RuntimeError("image unavailable")
            with patch.object(reseller_s0, "command", side_effect=primary):
                with self.assertRaises(RuntimeError) as caught:
                    reseller_s0.database_matrix(Path(directory), "synthetic", "postgres", False)
            self.assertIs(caught.exception, primary)
            evidence = json.loads((Path(directory) / "matrix-summary.json").read_text())[0]
            self.assertEqual(evidence["error"], "image unavailable")
            self.assertEqual(evidence["status"], "failed")


if __name__ == "__main__":
    unittest.main()
