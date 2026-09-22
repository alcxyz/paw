from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "ci" / "install-nix.sh"
ARCHIVE_SHA256 = "8132c325da027ccb158008c3b6745df7101b6883a491ecb8db5db3e281aa645b"
BASH = shutil.which("bash")
assert BASH is not None


class InstallNixTests(unittest.TestCase):
    def write_command(self, directory: Path, name: str, body: str) -> None:
        path = directory / name
        path.write_text(f"#!{BASH}\nset -euo pipefail\n" + body)
        path.chmod(0o755)

    def environment(self, root: Path, **overrides: str) -> dict[str, str]:
        fake_bin = root / "bin"
        fake_bin.mkdir()
        for name in ("awk", "chmod", "cp", "mkdir", "mktemp", "rm", "sh"):
            target = shutil.which(name)
            assert target is not None
            (fake_bin / name).symlink_to(target)
        runner_temp = root / "runner-temp"
        runner_temp.mkdir()
        github_path = root / "github-path"
        github_env = root / "github-env"
        install_log = root / "install-log"
        config_log = root / "config-log"
        token_log = root / "token-log"

        self.write_command(
            fake_bin,
            "uname",
            '[[ ${1:-} == -s ]] && printf "Linux\\n" || printf "x86_64\\n"\n',
        )
        self.write_command(
            fake_bin,
            "systemctl",
            '[[ ${FAKE_SYSTEMD_FAILURE:-0} != 1 ]]\n',
        )
        self.write_command(
            fake_bin,
            "curl",
            textwrap.dedent(
                """\
                output=
                while (($#)); do
                  if [[ $1 == --output ]]; then
                    output=$2
                    shift 2
                  else
                    shift
                  fi
                done
                printf archive > "$output"
                """
            ),
        )
        self.write_command(
            fake_bin,
            "sha256sum",
            'printf "%s  %s\\n" "${FAKE_ARCHIVE_SHA256}" "$1"\n',
        )
        self.write_command(
            fake_bin,
            "tar",
            textwrap.dedent(
                """\
                destination=
                while (($#)); do
                  if [[ $1 == -C ]]; then
                    destination=$2
                    shift 2
                  else
                    shift
                  fi
                done
                install_dir="$destination/nix-2.29.2-x86_64-linux"
                mkdir -p "$install_dir"
                printf '%s\\n' '#!BASH_PATH' \\
                  'printf "%s\\n" "$*" > "$FAKE_INSTALL_LOG"' \\
                  'last_argument=' \\
                  'for argument do last_argument=$argument; done' \\
                  'cp "$last_argument" "$FAKE_CONFIG_LOG"' \\
                  'printf "%s|%s|%s|%s\\n" "${GITHUB_TOKEN-unset}" "${GH_TOKEN-unset}" "${INPUT_GITHUB_ACCESS_TOKEN-unset}" "${NIX_CONFIG-unset}" > "$FAKE_TOKEN_LOG"' \\
                  'exit "${FAKE_INSTALL_FAILURE:-0}"' > "$install_dir/install"
                chmod +x "$install_dir/install"
                """
            ).replace("BASH_PATH", BASH),
        )

        env = {
            "PATH": str(fake_bin),
            "GITHUB_ACTIONS": "true",
            "RUNNER_ENVIRONMENT": "github-hosted",
            "RUNNER_OS": "Linux",
            "RUNNER_ARCH": "X64",
            "RUNNER_TEMP": str(runner_temp),
            "GITHUB_PATH": str(github_path),
            "GITHUB_ENV": str(github_env),
            "GITHUB_TOKEN": "repository-token",
            "GH_TOKEN": "cli-token",
            "INPUT_GITHUB_ACCESS_TOKEN": "action-token",
            "NIX_CONFIG": "access-tokens = github.com=bad",
            "FAKE_ARCHIVE_SHA256": ARCHIVE_SHA256,
            "FAKE_INSTALL_LOG": str(install_log),
            "FAKE_CONFIG_LOG": str(config_log),
            "FAKE_TOKEN_LOG": str(token_log),
        }
        env.update(overrides)
        return env

    def run_script(self, env: dict[str, str]) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [BASH, SCRIPT],
            cwd=ROOT,
            env=env,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=10,
        )

    def test_installs_verified_multi_user_nix_without_tokens(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = self.environment(root)
            result = self.run_script(env)
            self.assertEqual(result.returncode, 0, result.stderr)
            args = Path(env["FAKE_INSTALL_LOG"]).read_text()
            for option in (
                "--daemon",
                "--yes",
                "--no-channel-add",
                "--no-modify-profile",
                "--daemon-user-count 4",
                "--nix-extra-conf-file",
            ):
                self.assertIn(option, args)
            self.assertEqual(
                Path(env["FAKE_CONFIG_LOG"]).read_text(),
                "experimental-features = nix-command flakes\n"
                "sandbox = true\n"
                "sandbox-fallback = false\n"
                "max-jobs = 2\n"
                "cores = 2\n",
            )
            self.assertEqual(Path(env["FAKE_TOKEN_LOG"]).read_text(), "unset|unset|unset|unset\n")
            self.assertEqual(Path(env["GITHUB_PATH"]).read_text(), "/nix/var/nix/profiles/default/bin\n")
            self.assertEqual(Path(env["GITHUB_ENV"]).read_text(), "NIX_REMOTE=daemon\n")
            self.assertEqual(list((Path(env["RUNNER_TEMP"])).iterdir()), [])

    def test_refuses_non_github_hosted_or_wrong_platform(self):
        cases = {
            "not-actions": {"GITHUB_ACTIONS": "false"},
            "self-hosted": {"RUNNER_ENVIRONMENT": "self-hosted"},
            "wrong-os": {"RUNNER_OS": "Windows"},
            "wrong-architecture": {"RUNNER_ARCH": "ARM64"},
        }
        for name, overrides in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                env = self.environment(Path(directory), **overrides)
                result = self.run_script(env)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(Path(env["FAKE_INSTALL_LOG"]).exists())

    def test_hash_mismatch_stops_before_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.environment(Path(directory), FAKE_ARCHIVE_SHA256="0" * 64)
            result = self.run_script(env)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("SHA-256 mismatch", result.stderr)
            self.assertFalse(Path(env["FAKE_INSTALL_LOG"]).exists())

    def test_existing_nix_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = self.environment(root)
            self.write_command(Path(env["PATH"]), "nix", "exit 0\n")
            result = self.run_script(env)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("already installed", result.stderr)
            self.assertFalse(Path(env["FAKE_INSTALL_LOG"]).exists())

    def test_systemd_or_installer_failure_propagates(self):
        cases = {
            "systemd": {"FAKE_SYSTEMD_FAILURE": "1"},
            "installer": {"FAKE_INSTALL_FAILURE": "23"},
        }
        for name, overrides in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                env = self.environment(Path(directory), **overrides)
                result = self.run_script(env)
                self.assertNotEqual(result.returncode, 0)
                if name == "systemd":
                    self.assertFalse(Path(env["FAKE_INSTALL_LOG"]).exists())

    def test_script_pins_the_verified_official_archive(self):
        text = SCRIPT.read_text()
        self.assertIn(
            "https://releases.nixos.org/nix/nix-${nix_version}/${archive_name}",
            text,
        )
        self.assertIn(f"archive_sha256={ARCHIVE_SHA256}", text)
        self.assertNotIn("github.com/NixOS", text)


if __name__ == "__main__":
    unittest.main()
