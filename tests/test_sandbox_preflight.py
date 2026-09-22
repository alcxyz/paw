#!/usr/bin/env python3
"""Offline contract tests for the Linux sandbox preflight wrapper."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess
import shutil
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "ci" / "check-sandbox.sh"


class SandboxPreflightTests(unittest.TestCase):
    def run_probe(self, *, exit_status: int = 0) -> tuple[subprocess.CompletedProcess[str], list[bytes], Path]:
        with tempfile.TemporaryDirectory() as directory:
            fake_bin = Path(directory) / "bin"
            fake_bin.mkdir()
            log = Path(directory) / "args"
            fake = fake_bin / "nix-build"
            fake.write_text(
                "#!" + shutil.which("bash") + "\n" + """
set -euo pipefail
printf '%s\\0' "$@" > "$FAKE_NIX_BUILD_ARGS"
marker=
while (($#)); do
  if [[ $1 == --argstr && $2 == marker ]]; then marker=$3; shift 3; continue; fi
  shift
done
test -n "$marker"
test -s "$marker"
exit "${FAKE_NIX_BUILD_STATUS}"
"""
            )
            fake.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = f"{fake_bin}:{env['PATH']}"
            env["FAKE_NIX_BUILD_ARGS"] = str(log)
            env["FAKE_NIX_BUILD_STATUS"] = str(exit_status)
            result = subprocess.run(
                ["bash", str(SCRIPT)],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
            )
            args = Path(log).read_bytes().split(b"\0")[:-1]
            marker = next(
                value.decode()
                for index, value in enumerate(args)
                if value == b"--argstr" and args[index + 1] == b"marker"
                for value in [args[index + 2]]
            )
            return result, args, Path(marker)

    def test_passes_required_local_sandbox_options_and_cleans_marker(self):
        result, args, marker = self.run_probe()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists())
        self.assertIn(b"--option", args)
        self.assertIn(b"sandbox", args)
        self.assertIn(b"true", args)
        self.assertIn(b"sandbox-fallback", args)
        self.assertIn(b"false", args)
        self.assertIn(b"builders", args)
        self.assertIn(b"", args)
        self.assertIn(b"--argstr", args)
        self.assertIn(b"sourceRoot", args)

    def test_forwards_build_failure_and_cleans_marker(self):
        result, _, marker = self.run_probe(exit_status=37)
        self.assertEqual(result.returncode, 37)
        self.assertFalse(marker.exists())

    def test_script_and_expression_have_required_contract(self):
        script = SCRIPT.read_text()
        expression = (ROOT / "scripts/ci/sandbox-probe.nix").read_text()
        for option in ('--option sandbox true', '--option sandbox-fallback false', '--option builders ""'):
            self.assertIn(option, script)
        self.assertIn("builtins.getFlake sourceRoot", expression)
        self.assertIn("test ! -e", expression)
        self.assertIn("/proc/self/ns/mnt", expression)
        self.assertIn("/proc/self/ns/pid", expression)
        self.assertIn("/proc/self/ns/net", expression)


if __name__ == "__main__":
    unittest.main()
