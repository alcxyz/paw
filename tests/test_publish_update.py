import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock
import urllib.error


SCRIPT = Path(__file__).parents[1] / "scripts" / "ci" / "publish-update.py"
ASKPASS = SCRIPT.with_name("git-askpass.py")
SPEC = importlib.util.spec_from_file_location("publish_update", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
publish_update = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = publish_update
SPEC.loader.exec_module(publish_update)


def environment(**overrides):
    result = {
        "GITHUB_SERVER_URL": "https://github.com",
        "GITHUB_API_URL": "https://api.github.com",
        "GITHUB_REPOSITORY": "alcxyz/paw",
        "GITHUB_TOKEN": "test-token",
    }
    result.update(overrides)
    return result


class FakeClient:
    def __init__(self, *, pulls=None, remote_head="a" * 40):
        self.pulls = [] if pulls is None else pulls
        self.remote_head = remote_head
        self.created = []
        self.existing_branches = set()

    def repository(self):
        return {"id": 42, "full_name": "alcxyz/paw", "private": False}

    def open_pulls_page(self, base, page):
        return self.pulls if page == 1 else []

    def branch(self, name):
        return {"commit": {"sha": self.remote_head}}

    def create_pull(self, base, branch):
        self.created.append((base, branch))

    def branch_exists(self, name):
        return name in self.existing_branches


class FakeGit:
    def __init__(self, *, branch="dev", head="a" * 40, paths=None):
        self.branch = branch
        self.head_value = head
        self.paths = set() if paths is None else set(paths)
        self.published = []

    def current_branch(self):
        return self.branch

    def head(self):
        return self.head_value

    def changed_paths(self):
        return self.paths

    def publish(self, config, paths, branch):
        self.published.append((config, paths, branch))


class PublishUpdateTests(unittest.TestCase):
    def args(self, check_open=False):
        return types.SimpleNamespace(check_open=check_open)

    def run_with(self, client, git, *, args=None, env=None, root=None):
        if args is None:
            args = self.args()
        if env is None:
            env = environment()
        if root is None:
            root = Path(tempfile.mkdtemp())
        with mock.patch.object(publish_update, "GitHubClient", return_value=client), mock.patch.object(
            publish_update, "Git", return_value=git
        ):
            return publish_update.run(args, env, root)

    def test_no_changes_is_successful_no_op(self):
        client = FakeClient()
        git = FakeGit()
        self.assertEqual(self.run_with(client, git), 0)
        self.assertEqual(git.published, [])
        self.assertEqual(client.created, [])

    def test_github_origins_default_for_a_safe_repository(self):
        config = publish_update.Config.from_env(
            {
                "GITHUB_REPOSITORY": "alcxyz/paw",
                "GITHUB_TOKEN": "test-token",
            }
        )
        self.assertEqual(config.server_url, "https://github.com")
        self.assertEqual(config.api_url, "https://api.github.com")

    def test_private_fork_is_accepted_when_repository_metadata_matches(self):
        client = publish_update.GitHubClient(
            publish_update.Config.from_env(
                environment(GITHUB_REPOSITORY="fork-owner/paw")
            )
        )
        metadata = {"id": 42, "full_name": "fork-owner/paw", "private": True}
        with mock.patch.object(client, "request", return_value=metadata):
            self.assertEqual(client.repository(), metadata)

    def test_unexpected_repository_metadata_is_rejected(self):
        client = publish_update.GitHubClient(
            publish_update.Config.from_env(environment())
        )
        metadata = {"id": 42, "full_name": "other/paw", "private": False}
        with mock.patch.object(client, "request", return_value=metadata):
            with self.assertRaises(publish_update.PublishError):
                client.repository()

    def test_existing_owned_dependency_pr_skips_publication(self):
        pulls = [
            {
                "head": {
                    "ref": "automation/dependencies-abcd",
                    "repo": {"id": 42},
                },
                "base": {"ref": "dev"},
            }
        ]
        client = FakeClient(pulls=pulls)
        git = FakeGit(paths={"nix/packages/t3code/source.json"})
        self.assertEqual(self.run_with(client, git), 0)
        self.assertEqual(git.published, [])
        self.assertEqual(client.created, [])

    def test_same_branch_name_from_fork_does_not_block(self):
        client = FakeClient(
            pulls=[
                {
                    "head": {
                        "ref": "automation/dependencies-abcd",
                        "repo": {"id": 999},
                    },
                    "base": {"ref": "dev"},
                }
            ]
        )
        self.assertFalse(publish_update.has_open_update(client, 42, "dev"))

    def test_check_open_writes_boolean_output(self):
        pulls = [
            {
                "head": {
                    "ref": "automation/dependencies-abcd",
                    "repo": {"id": 42},
                },
                "base": {"ref": "dev"},
            }
        ]
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            env = environment(GITHUB_OUTPUT=str(output))
            self.assertEqual(
                self.run_with(FakeClient(pulls=pulls), FakeGit(), args=self.args(True), env=env),
                0,
            )
            self.assertEqual(output.read_text(), "open=true\n")

    def test_unknown_and_untracked_paths_are_rejected(self):
        git = publish_update.Git(Path("."))
        cases = [
            b" M README.md\0",
            b"?? nix/packages/t3code/source.json\0",
        ]
        for status in cases:
            with self.subTest(status=status), mock.patch.object(git, "run", return_value=status):
                with self.assertRaises(publish_update.PublishError):
                    git.changed_paths()

    def test_allowlisted_modified_paths_are_accepted(self):
        git = publish_update.Git(Path("."))
        status = (
            b" M nix/packages/t3code/source.json\0"
            b"M  nix/packages/codex-cli/package-lock.json\0"
        )
        with mock.patch.object(git, "run", return_value=status):
            self.assertEqual(
                git.changed_paths(),
                {
                    "nix/packages/t3code/source.json",
                    "nix/packages/codex-cli/package-lock.json",
                },
            )

    def test_base_branch_mismatch_is_rejected(self):
        with self.assertRaisesRegex(publish_update.PublishError, "current branch"):
            self.run_with(FakeClient(), FakeGit(branch="main"))

    def test_remote_base_head_mismatch_is_rejected(self):
        with self.assertRaisesRegex(publish_update.PublishError, "local HEAD"):
            self.run_with(FakeClient(remote_head="b" * 40), FakeGit())

    def test_placeholder_hash_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "nix/packages/t3code/source.json"
            target.parent.mkdir(parents=True)
            target.write_text(
                '{"hash":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}\n'
            )
            with self.assertRaisesRegex(publish_update.PublishError, "placeholder hash"):
                publish_update.verify_files(root, {"nix/packages/t3code/source.json"})

    def test_existing_candidate_branch_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "nix/packages/t3code/source.json"
            target.parent.mkdir(parents=True)
            target.write_text('{"hash":"sha256-YWJjZGVmZ2hpamtsbW5vcA=="}\n')
            client = FakeClient()
            candidate = publish_update.verify_files(
                root, {"nix/packages/t3code/source.json"}
            )
            client.existing_branches.add(
                f"automation/dependencies-{candidate}-aaaaaaaa"
            )
            git = FakeGit(paths={"nix/packages/t3code/source.json"})
            with self.assertRaisesRegex(publish_update.PublishError, "already exists"):
                self.run_with(client, git, root=root)
            self.assertEqual(git.published, [])

    def test_publish_sets_identity_disables_hooks_redirects_and_helpers(self):
        git = publish_update.Git(Path("."))
        calls = []

        def capture(args, **kwargs):
            calls.append((list(args), kwargs))
            return b""

        with mock.patch.object(git, "run", side_effect=capture):
            git.publish(
                publish_update.Config.from_env(environment()),
                {"nix/packages/t3code/source.json"},
                "automation/dependencies-candidate-aaaaaaaa",
            )
        commit = calls[2][0]
        push = calls[3][0]
        self.assertIn("user.name=paw-update-bot", commit)
        self.assertIn("user.email=paw-update-bot@users.noreply.invalid", commit)
        self.assertIn("core.hooksPath=/dev/null", commit)
        self.assertIn("credential.helper=", push)
        self.assertIn("http.extraHeader=", push)
        self.assertIn("http.followRedirects=false", push)
        self.assertNotIn("test-token", " ".join(push))
        self.assertEqual(calls[3][1]["env"]["GITHUB_TOKEN"], "test-token")

    def test_github_api_routing_headers_query_and_payload(self):
        class Response:
            status = 201

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

            def read(self, _limit):
                return b'{"number":1}'

        client = publish_update.GitHubClient(
            publish_update.Config.from_env(environment())
        )
        client.opener = mock.Mock()
        client.opener.open.return_value = Response()
        value = client.request(
            "POST",
            "/repos/alcxyz/paw/pulls",
            query={"page": 2, "per_page": 50},
            payload={"base": "dev", "head": "automation/dependencies-test"},
            expected=(201,),
        )
        self.assertEqual(value, {"number": 1})
        request = client.opener.open.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "https://api.github.com/repos/alcxyz/paw/pulls?page=2&per_page=50",
        )
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.get_header("Accept"), "application/vnd.github+json")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-token")
        self.assertEqual(request.get_header("X-github-api-version"), "2022-11-28")
        self.assertEqual(
            json.loads(request.data),
            {"base": "dev", "head": "automation/dependencies-test"},
        )
        self.assertNotIn("test-token", request.full_url)

    def test_askpass_uses_checkout_compatible_token_username(self):
        result = subprocess.run(
            [sys.executable, ASKPASS, "Username for 'https://github.com':"],
            check=False,
            env={},
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "x-access-token\n")

        result = subprocess.run(
            [sys.executable, ASKPASS, "Password for 'https://x-access-token@github.com':"],
            check=False,
            env={"GITHUB_TOKEN": "test-token"},
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "test-token\n")

    def test_http_error_reports_status_without_response_body(self):
        config = publish_update.Config.from_env(environment())
        client = publish_update.GitHubClient(config)
        error = urllib.error.HTTPError(
            "https://api.github.com/repos/alcxyz/paw",
            500,
            "failure",
            {},
            io.BytesIO(b"sensitive server response"),
        )
        client.opener = mock.Mock()
        client.opener.open.side_effect = error
        with self.assertRaises(publish_update.PublishError) as caught:
            client.request("GET", "/repos/alcxyz/paw")
        self.assertEqual(str(caught.exception), "GitHub API returned HTTP 500")
        self.assertNotIn("sensitive", str(caught.exception))
        self.assertTrue(error.fp.closed)

    def test_rejects_non_origin_server_url_and_unsafe_base(self):
        for value in (
            "http://github.com",
            "https://token@github.com",
            "https://github.com/subpath",
            "https://github.com?token=value",
        ):
            with self.subTest(value=value), self.assertRaises(publish_update.PublishError):
                publish_update.Config.from_env(environment(GITHUB_SERVER_URL=value))
        with self.assertRaises(publish_update.PublishError):
            publish_update.Config.from_env(environment(GITHUB_API_URL="https://example.test"))
        with self.assertRaises(publish_update.PublishError):
            publish_update.Config.from_env(environment(GITHUB_REPOSITORY="other/paw/extra"))
        with self.assertRaises(publish_update.PublishError):
            publish_update.Config.from_env(environment(BASE_BRANCH="../main"))


if __name__ == "__main__":
    unittest.main()
