#!/usr/bin/env python3
"""Offline regression tests for the bounded dependency updater."""

from __future__ import annotations

import importlib.util
from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).parents[1] / "scripts/update-dependencies.py"
SPEC = importlib.util.spec_from_file_location("update_dependencies", SCRIPT)
assert SPEC and SPEC.loader
updater = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = updater
SPEC.loader.exec_module(updater)

HASH_A = "sha256-AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
HASH_B = "sha256-AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI="
HASH_C = "sha256-AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="


def t3_pin(**updates):
    return {
        "owner": "pingdotgg",
        "repo": "t3code",
        "rev": "a" * 40,
        "hash": HASH_A,
        "version": "1.0.0",
        "cargoHash": HASH_A,
        "pnpmDepsHash": HASH_A,
    } | updates


def codex_pin(**updates):
    return {"version": "1.0.0", "hash": HASH_A, "npmDepsHash": HASH_A} | updates


def optional_dependencies(version):
    return {name: version for name in updater.CODEX_OPTIONAL_PACKAGES}


def codex_lock(version):
    return {
        "name": "@openai/codex",
        "version": version,
        "lockfileVersion": 3,
        "packages": {
            "": {
                "name": "@openai/codex",
                "version": version,
                "optionalDependencies": optional_dependencies(version),
            }
        },
    }


def codex_archive(version):
    metadata = {
        "name": "@openai/codex",
        "version": version,
        "bin": {"codex": "bin/codex.js"},
        "optionalDependencies": optional_dependencies(version),
    }
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as archive:
        for name, content in (
            ("package/package.json", json.dumps(metadata).encode()),
            ("package/bin/codex.js", b"#!/usr/bin/env node\n"),
        ):
            member = tarfile.TarInfo(name)
            member.size = len(content)
            member.mode = 0o755 if name.endswith(".js") else 0o644
            archive.addfile(member, io.BytesIO(content))
    return output.getvalue()


class FakeHTTP:
    def __init__(self, json_values, byte_values=None):
        self.json_values = json_values
        self.byte_values = byte_values or {}
        self.calls = []

    def json(self, url, *, max_bytes=64 * 1024 * 1024):
        self.calls.append(("json", url))
        value = self.json_values[url]
        if isinstance(value, Exception):
            raise value
        return value

    def bytes(self, url, *, max_bytes=256 * 1024 * 1024):
        self.calls.append(("bytes", url))
        value = self.byte_values[url]
        if isinstance(value, Exception):
            raise value
        return value


class FakeRunner:
    def __init__(self, *, fail_attribute=None):
        self.calls = []
        self.fail_attribute = fail_attribute

    @staticmethod
    def derivation(attribute):
        label = "cargo" if "cargoDeps" in attribute else "pnpm" if "pnpmDeps" in attribute else "npm"
        return f"/nix/store/00000000000000000000000000000000-{label}.drv"

    def run(self, args, *, cwd, env=None, timeout=1800):
        args = tuple(args)
        self.calls.append((args, Path(cwd), dict(env) if env is not None else None, timeout))
        if args[:4] == ("nix", "store", "prefetch-file", "--unpack"):
            return updater.CommandResult(0, json.dumps({"hash": HASH_A}))
        if args[:3] == ("nix", "eval", "--raw"):
            attribute = args[3].removesuffix(".drvPath")
            return updater.CommandResult(0, self.derivation(attribute))
        if args[:2] == ("nix", "build"):
            attribute = args[2]
            if attribute == self.fail_attribute:
                unrelated = "/nix/store/11111111111111111111111111111111-source.drv"
                diagnostic = (
                    "error: hash mismatch in fixed-output derivation "
                    f"'{unrelated}':\n  got: {HASH_B}\n"
                    f"error: Cannot build {self.derivation(attribute)} because a dependency failed\n"
                )
                return updater.CommandResult(1, stderr=diagnostic)
            derived = HASH_B if "cargoDeps" in attribute else HASH_C
            diagnostic = (
                "error: hash mismatch in fixed-output derivation "
                f"'{self.derivation(attribute)}':\n  got: {derived}\n"
            )
            return updater.CommandResult(1, stderr=diagnostic)
        if args[:2] == ("nix", "shell"):
            self._generate_lock(Path(cwd), env, args)
            return updater.CommandResult(0)
        raise AssertionError(f"unexpected command: {args}")

    def _generate_lock(self, package, env, args):
        self.assert_isolated_npm(env, args)
        version = json.loads((package / "package.json").read_text())["version"]
        (package / "package-lock.json").write_text(json.dumps(codex_lock(version), indent=2) + "\n")

    @staticmethod
    def assert_isolated_npm(env, args):
        assert env is not None
        assert env["npm_config_ignore_scripts"] == "true"
        assert env["npm_config_registry"] == "https://registry.npmjs.org/"
        assert env["npm_config_userconfig"].endswith("empty-npmrc")
        assert env["npm_config_globalconfig"].endswith("empty-global-npmrc")
        assert "--ignore-scripts" in args
        assert args[:5] == (
            "nix",
            "shell",
            "--no-write-lock-file",
            "--inputs-from",
            args[4],
        )
        assert Path(args[4]).is_absolute()
        assert args[5:] == (
            "nixpkgs#nodejs_24",
            "--command",
            "npm",
            "install",
            "--package-lock-only",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
        )


class Checkout:
    def __init__(self, directory, t3=None, codex=None):
        self.root = Path(directory)
        self.t3_path = self.root / "nix/packages/t3code/source.json"
        self.codex_path = self.root / "nix/packages/codex-cli/source.json"
        self.lock_path = self.root / "nix/packages/codex-cli/package-lock.json"
        self.t3_path.parent.mkdir(parents=True)
        self.codex_path.parent.mkdir(parents=True)
        self.t3_path.write_text(json.dumps(t3 or t3_pin()))
        self.codex_path.write_text(json.dumps(codex or codex_pin()))
        self.lock_path.write_text(json.dumps(codex_lock((codex or codex_pin())["version"])))

    def originals(self):
        return {path: path.read_bytes() for path in (self.t3_path, self.codex_path, self.lock_path)}


def release(version, **updates):
    return {
        "tag_name": "v" + version,
        "draft": False,
        "prerelease": False,
    } | updates


def response_map(t3_version="1.1.0", revision="b" * 40, codex_version="1.1.0"):
    tag_url = (
        "https://api.github.com/repos/pingdotgg/t3code/git/ref/tags/v" + t3_version
    )
    codex_url = f"https://registry.npmjs.org/@openai/codex/-/codex-{codex_version}.tgz"
    return (
        {
            updater.T3_RELEASES_URL: [release(t3_version)],
            tag_url: {"object": {"type": "commit", "sha": revision}},
            updater.CODEX_REGISTRY_URL: {
                "dist-tags": {"latest": codex_version},
                "versions": {
                    codex_version: {"dist": {"tarball": codex_url}},
                },
            },
        },
        {codex_url: codex_archive(codex_version)},
    )


class SelectionTests(unittest.TestCase):
    def test_selects_highest_stable_release_only(self):
        releases = [
            release("1.2.0"),
            release("9.0.0", prerelease=True),
            release("8.0.0", draft=True),
            release("7.0.0-nightly.20260922.1"),
            release("1.10.0"),
        ]
        self.assertEqual(updater.select_stable_t3_release(releases), "1.10.0")

    def test_rejects_prerelease_codex_latest(self):
        with self.assertRaisesRegex(updater.UpdateError, "stable semver"):
            updater.stable_codex_version(
                {
                    "dist-tags": {"latest": "1.2.0-beta.1"},
                    "versions": {"1.2.0-beta.1": {}},
                }
            )

    def test_rejects_placeholder_hash(self):
        with self.assertRaisesRegex(updater.UpdateError, "placeholder"):
            updater.validate_sri(updater.FAKE_HASH)

    def test_malformed_lock_fails_with_validation_error(self):
        malformed = json.dumps({"lockfileVersion": 3, "packages": []}).encode()
        with self.assertRaises(updater.UpdateError):
            updater.validate_codex_lock(malformed, "1.0.0")


class ProposalTests(unittest.TestCase):
    def test_unchanged_is_noop_without_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            values, archives = response_map("1.0.0", "a" * 40, "1.0.0")
            runner = FakeRunner()
            changed = updater.Updater(
                checkout.root, FakeHTTP(values, archives), runner
            ).propose()
            self.assertEqual(changed, [])
            self.assertEqual(runner.calls, [])

    def test_updates_both_pins_with_complete_hashes(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            values, archives = response_map()
            runner = FakeRunner()
            changed = updater.Updater(
                checkout.root, FakeHTTP(values, archives), runner
            ).propose()
            self.assertEqual(changed, ["t3code", "codex"])
            t3 = json.loads(checkout.t3_path.read_text())
            codex = json.loads(checkout.codex_path.read_text())
            self.assertEqual(t3["version"], "1.1.0")
            self.assertEqual(t3["rev"], "b" * 40)
            self.assertEqual(t3["cargoHash"], HASH_B)
            self.assertEqual(t3["pnpmDepsHash"], HASH_C)
            self.assertEqual(codex, {"version": "1.1.0", "hash": HASH_A, "npmDepsHash": HASH_C})
            self.assertNotIn(updater.FAKE_HASH, checkout.t3_path.read_text())
            self.assertNotIn(updater.FAKE_HASH, checkout.codex_path.read_text())
            updater.validate_codex_lock(checkout.lock_path.read_bytes(), "1.1.0")

    def test_dependency_hash_failure_restores_every_file(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            originals = checkout.originals()
            values, archives = response_map()
            runner = FakeRunner(
                fail_attribute=".#t3code-headless.resourceMonitor.cargoDeps"
            )
            with self.assertRaises(updater.DependencyHashError):
                updater.Updater(checkout.root, FakeHTTP(values, archives), runner).propose()
            self.assertEqual(checkout.originals(), originals)

    def test_network_failure_happens_before_any_write(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            originals = checkout.originals()
            values, archives = response_map()
            values[updater.CODEX_REGISTRY_URL] = updater.UpdateError("offline")
            with self.assertRaisesRegex(updater.UpdateError, "offline"):
                updater.Updater(
                    checkout.root, FakeHTTP(values, archives), FakeRunner()
                ).propose()
            self.assertEqual(checkout.originals(), originals)

    def test_refuses_to_replace_custom_t3_source(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory, t3=t3_pin(owner="example"))
            values, archives = response_map()
            with self.assertRaisesRegex(updater.UpdateError, "custom T3 source"):
                updater.Updater(
                    checkout.root, FakeHTTP(values, archives), FakeRunner()
                ).propose()

    def test_moved_release_tag_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            values, archives = response_map("1.0.0", "b" * 40, "1.0.0")
            with self.assertRaisesRegex(updater.UpdateError, "tag moved"):
                updater.Updater(
                    checkout.root, FakeHTTP(values, archives), FakeRunner()
                ).propose()

    def test_check_reports_candidates_without_commands_or_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            checkout = Checkout(directory)
            originals = checkout.originals()
            values, archives = response_map()
            runner = FakeRunner()
            report = updater.Updater(
                checkout.root, FakeHTTP(values, archives), runner
            ).check()
            self.assertTrue(report["t3code"]["updateAvailable"])
            self.assertTrue(report["codex"]["updateAvailable"])
            self.assertEqual(runner.calls, [])
            self.assertEqual(checkout.originals(), originals)


class GuardTests(unittest.TestCase):
    class GitRunner:
        def __init__(self, root, status=""):
            self.root = root
            self.status = status

        def run(self, args, *, cwd, env=None, timeout=1800):
            if "rev-parse" in args:
                return updater.CommandResult(0, str(self.root) + "\n")
            if "status" in args:
                return updater.CommandResult(0, self.status)
            raise AssertionError(args)

    def test_write_mode_requires_explicit_disposable_ci_checkout(self):
        root = Path("/tmp/example")
        runner = self.GitRunner(root)
        with self.assertRaises(updater.PreconditionError):
            updater.enforce_ci_checkout(root, runner, {})
        updater.enforce_ci_checkout(
            root,
            runner,
            {"CI": "true", "PAW_DISPOSABLE_UPDATE_CHECKOUT": "1"},
        )

    def test_write_mode_rejects_dirty_checkout(self):
        root = Path("/tmp/example")
        with self.assertRaisesRegex(updater.PreconditionError, "clean"):
            updater.enforce_ci_checkout(
                root,
                self.GitRunner(root, " M flake.nix\n"),
                {"CI": "true", "PAW_DISPOSABLE_UPDATE_CHECKOUT": "1"},
            )

    def test_dependency_hash_failure_has_distinct_exit_status(self):
        root = Path("/tmp/example")
        error = updater.DependencyHashError("missing dependency hash")
        with mock.patch.object(updater.Updater, "propose", side_effect=error):
            with redirect_stderr(io.StringIO()):
                status = updater.main(
                    [],
                    root=root,
                    env={"CI": "true", "PAW_DISPOSABLE_UPDATE_CHECKOUT": "1"},
                    runner=self.GitRunner(root),
                    http=FakeHTTP({}),
                )
        self.assertEqual(status, updater.EXIT_DEPENDENCY_HASH)
        self.assertNotIn(status, {updater.EXIT_ERROR, updater.EXIT_PRECONDITION, 2})


if __name__ == "__main__":
    unittest.main()
