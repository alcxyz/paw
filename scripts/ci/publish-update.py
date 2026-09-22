#!/usr/bin/env python3
"""Publish a narrowly constrained dependency update pull request to GitHub."""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import os
from pathlib import Path
import re
import ssl
import stat
import subprocess
import sys
from typing import Any, Mapping, Sequence
import urllib.error
import urllib.parse
import urllib.request


ALLOWED_PATHS = frozenset(
    {
        "nix/packages/t3code/source.json",
        "nix/packages/codex-cli/source.json",
        "nix/packages/codex-cli/package-lock.json",
    }
)
BRANCH_PREFIX = "automation/dependencies-"
PAGE_SIZE = 50
MAX_PAGES = 10
MAX_RESPONSE_BYTES = 2 * 1024 * 1024
EXPECTED_SERVER_URL = "https://github.com"
EXPECTED_API_URL = "https://api.github.com"


class PublishError(Exception):
    """A safe-to-display publication failure."""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        return None


class Config:
    def __init__(
        self,
        server_url: str,
        api_url: str,
        repository: str,
        token: str,
        base_branch: str,
    ) -> None:
        self.server_url = server_url
        self.api_url = api_url
        self.repository = repository
        self.token = token
        self.base_branch = base_branch

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> "Config":
        server_url = validate_origin(
            env.get("GITHUB_SERVER_URL", EXPECTED_SERVER_URL), "GITHUB_SERVER_URL"
        )
        if server_url != EXPECTED_SERVER_URL:
            raise PublishError("GITHUB_SERVER_URL must be https://github.com")
        api_url = validate_origin(
            env.get("GITHUB_API_URL", EXPECTED_API_URL), "GITHUB_API_URL"
        )
        if api_url != EXPECTED_API_URL:
            raise PublishError("GITHUB_API_URL must be https://api.github.com")
        repository = env.get("GITHUB_REPOSITORY", "")
        parts = repository.split("/")
        if len(parts) != 2 or any(
            not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,99}", part)
            or part in {".", ".."}
            for part in parts
        ):
            raise PublishError("GITHUB_REPOSITORY must be an owner/name pair")
        token = env.get("GITHUB_TOKEN", "")
        if not token:
            raise PublishError("GITHUB_TOKEN is required")
        base_branch = env.get("BASE_BRANCH", "dev")
        validate_branch(base_branch, "BASE_BRANCH")
        return cls(server_url, api_url, repository, token, base_branch)


def validate_origin(value: str, label: str) -> str:
    try:
        parsed = urllib.parse.urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise PublishError(f"{label} is invalid") from exc
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise PublishError(f"{label} must be an HTTPS origin")
    hostname = parsed.hostname
    assert hostname is not None
    host = f"[{hostname}]" if ":" in hostname else hostname
    if port is not None:
        host = f"{host}:{port}"
    return f"https://{host}"


def validate_branch(value: str, label: str = "branch") -> None:
    if (
        not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{0,127}", value)
        or ".." in value
        or "//" in value
        or "@{" in value
        or value.endswith(("/", ".", ".lock"))
        or any(part.startswith(".") or part.endswith(".lock") for part in value.split("/"))
    ):
        raise PublishError(f"{label} is not a safe branch name")


class GitHubClient:
    def __init__(self, config: Config) -> None:
        self.config = config
        self.api_root = config.api_url
        self.repo_path = "/".join(
            urllib.parse.quote(part, safe="") for part in config.repository.split("/")
        )
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPSHandler(context=ssl.create_default_context()), NoRedirect()
        )

    def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, str | int] | None = None,
        payload: Mapping[str, Any] | None = None,
        expected: Sequence[int] = (200,),
        not_found_ok: bool = False,
    ) -> Any:
        url = f"{self.api_root}{path}"
        if query:
            url += "?" + urllib.parse.urlencode(query)
        data = None
        headers = {
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {self.config.token}",
            "User-Agent": "paw-dependency-publisher/1",
            "X-GitHub-Api-Version": "2022-11-28",
        }
        if payload is not None:
            data = json.dumps(payload, separators=(",", ":")).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=30) as response:
                if response.status not in expected:
                    raise PublishError(f"GitHub API returned HTTP {response.status}")
                raw = response.read(MAX_RESPONSE_BYTES + 1)
        except urllib.error.HTTPError as exc:
            status = exc.code
            exc.close()
            if not_found_ok and status == 404:
                return None
            raise PublishError(f"GitHub API returned HTTP {status}") from None
        except urllib.error.URLError as exc:
            raise PublishError("GitHub API request failed") from None
        except OSError:
            raise PublishError("GitHub API request failed") from None
        if len(raw) > MAX_RESPONSE_BYTES:
            raise PublishError("GitHub API response was too large")
        try:
            return json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise PublishError("GitHub API returned invalid JSON") from None

    def repository(self) -> Mapping[str, Any]:
        value = self.request("GET", f"/repos/{self.repo_path}")
        if (
            not isinstance(value, dict)
            or not isinstance(value.get("id"), int)
            or value.get("full_name") != self.config.repository
        ):
            raise PublishError("GitHub repository metadata was invalid")
        return value

    def branch(self, name: str) -> Mapping[str, Any]:
        quoted = urllib.parse.quote(name, safe="")
        value = self.request("GET", f"/repos/{self.repo_path}/branches/{quoted}")
        if not isinstance(value, dict):
            raise PublishError("GitHub branch metadata was invalid")
        commit = value.get("commit")
        if not isinstance(commit, dict) or not isinstance(commit.get("sha"), str):
            raise PublishError("GitHub branch metadata was invalid")
        return value

    def open_pulls_page(self, base: str, page: int) -> list[Any]:
        value = self.request(
            "GET",
            f"/repos/{self.repo_path}/pulls",
            query={"state": "open", "base": base, "per_page": PAGE_SIZE, "page": page},
        )
        if not isinstance(value, list):
            raise PublishError("GitHub pull request metadata was invalid")
        return value

    def branch_exists(self, name: str) -> bool:
        quoted = urllib.parse.quote(name, safe="")
        value = self.request(
            "GET",
            f"/repos/{self.repo_path}/branches/{quoted}",
            not_found_ok=True,
        )
        if value is None:
            return False
        if not isinstance(value, dict):
            raise PublishError("GitHub branch metadata was invalid")
        return True

    def create_pull(self, base: str, branch: str) -> None:
        self.request(
            "POST",
            f"/repos/{self.repo_path}/pulls",
            payload={
                "base": base,
                "head": branch,
                "title": "chore: update pinned dependencies",
                "body": (
                    "Automated proposal for reviewed dependency pins. "
                    "Build and contract checks passed before publication. "
                    "This pull request does not deploy or promote an image."
                ),
            },
            expected=(201,),
        )


def has_open_update(client: GitHubClient, repository_id: int, base: str) -> bool:
    for page in range(1, MAX_PAGES + 1):
        pulls = client.open_pulls_page(base, page)
        for pull in pulls:
            if not isinstance(pull, dict):
                raise PublishError("GitHub pull request metadata was invalid")
            head = pull.get("head")
            pull_base = pull.get("base")
            if not isinstance(head, dict) or not isinstance(pull_base, dict):
                continue
            head_repo = head.get("repo")
            if (
                head.get("ref", "").startswith(BRANCH_PREFIX)
                and pull_base.get("ref") == base
                and isinstance(head_repo, dict)
                and head_repo.get("id") == repository_id
            ):
                return True
        if len(pulls) < PAGE_SIZE:
            return False
    raise PublishError("GitHub pull request pagination limit reached")


class Git:
    def __init__(self, root: Path) -> None:
        self.root = root

    def run(
        self,
        args: Sequence[str],
        *,
        env: Mapping[str, str] | None = None,
        label: str = "git command",
    ) -> bytes:
        try:
            result = subprocess.run(
                ["git", *args],
                cwd=self.root,
                env=env,
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=60,
            )
        except (OSError, subprocess.TimeoutExpired):
            raise PublishError(f"{label} could not run") from None
        if result.returncode != 0:
            raise PublishError(f"{label} failed")
        return result.stdout

    def current_branch(self) -> str:
        return self.run(
            ["symbolic-ref", "--quiet", "--short", "HEAD"], label="reading current branch"
        ).decode("utf-8", "strict").strip()

    def head(self) -> str:
        return self.run(["rev-parse", "--verify", "HEAD"], label="reading HEAD").decode().strip()

    def changed_paths(self) -> set[str]:
        raw = self.run(
            ["status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames"],
            label="reading worktree status",
        )
        changed: set[str] = set()
        for record in raw.split(b"\0"):
            if not record:
                continue
            if len(record) < 4 or record[2:3] != b" ":
                raise PublishError("git status output was invalid")
            status_code = record[:2].decode("ascii", "strict")
            path = record[3:].decode("utf-8", "surrogateescape")
            if status_code == "??":
                raise PublishError(f"untracked path is not publishable: {path}")
            if "D" in status_code or "A" in status_code or "U" in status_code:
                raise PublishError(f"unsupported change type for: {path}")
            changed.add(path)
        unknown = changed - ALLOWED_PATHS
        if unknown:
            raise PublishError(f"change outside dependency allowlist: {sorted(unknown)[0]}")
        return changed

    def publish(self, config: Config, paths: set[str], branch: str) -> None:
        askpass = Path(__file__).with_name("git-askpass.py").resolve()
        if not askpass.is_file():
            raise PublishError("git askpass helper is missing")
        self.run(["switch", "-c", branch], label="creating update branch")
        self.run(["add", "--", *sorted(paths)], label="staging dependency update")
        self.run(
            [
                "-c",
                "core.hooksPath=/dev/null",
                "-c",
                "user.name=paw-update-bot",
                "-c",
                "user.email=paw-update-bot@users.noreply.invalid",
                "commit",
                "-m",
                "chore: update pinned dependencies",
            ],
            label="committing dependency update",
        )
        env = dict(os.environ)
        env.update(
            {
                "GIT_ASKPASS": str(askpass),
                "GIT_TERMINAL_PROMPT": "0",
                "GITHUB_TOKEN": config.token,
            }
        )
        remote = f"{config.server_url}/{config.repository}.git"
        self.run(
            [
                "-c",
                "core.hooksPath=/dev/null",
                "-c",
                "credential.helper=",
                "-c",
                "http.extraHeader=",
                "-c",
                "http.followRedirects=false",
                "push",
                "--porcelain",
                remote,
                f"HEAD:refs/heads/{branch}",
            ],
            env=env,
            label="pushing update branch",
        )


def verify_files(root: Path, paths: set[str]) -> str:
    digest = hashlib.sha256()
    for relative in sorted(paths):
        path = root / relative
        try:
            mode = path.lstat().st_mode
            content = path.read_bytes()
        except OSError:
            raise PublishError(f"updated file could not be read: {relative}") from None
        if not stat.S_ISREG(mode):
            raise PublishError(f"updated path is not a regular file: {relative}")
        digest.update(relative.encode())
        digest.update(b"\0")
        digest.update(content)
        digest.update(b"\0")
        if relative.endswith(".json"):
            reject_placeholder_hashes(relative, content)
    return digest.hexdigest()[:16]


def reject_placeholder_hashes(relative: str, content: bytes) -> None:
    try:
        value = json.loads(content)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise PublishError(f"updated JSON is invalid: {relative}") from None

    def walk(item: Any, key: str = "") -> None:
        if isinstance(item, dict):
            for child_key, child in item.items():
                walk(child, str(child_key))
        elif isinstance(item, list):
            for child in item:
                walk(child, key)
        elif isinstance(item, str) and re.search(r"hash|integrity", key, re.IGNORECASE):
            normalized = item.strip().lower()
            if (
                not normalized
                or "fakehash" in normalized
                or "placeholder" in normalized
                or "todo" in normalized
            ):
                raise PublishError(f"placeholder hash in: {relative}")
            for sri_hash in normalized.split():
                if "-" not in sri_hash:
                    continue
                encoded = sri_hash.split("-", 1)[1]
                unpadded = encoded.rstrip("=")
                if len(unpadded) >= 16 and set(unpadded) in ({"a"}, {"0"}):
                    raise PublishError(f"placeholder hash in: {relative}")
                try:
                    decoded = base64.b64decode(encoded, validate=True)
                except (binascii.Error, ValueError):
                    continue
                if decoded and not any(decoded):
                    raise PublishError(f"placeholder hash in: {relative}")

    walk(value)


def write_open_output(env: Mapping[str, str], value: bool) -> None:
    output = env.get("GITHUB_OUTPUT")
    if not output:
        return
    path = Path(output)
    try:
        with path.open("a", encoding="utf-8") as stream:
            stream.write(f"open={'true' if value else 'false'}\n")
    except OSError:
        raise PublishError("could not write GITHUB_OUTPUT") from None


def run(args: argparse.Namespace, env: Mapping[str, str], root: Path) -> int:
    config = Config.from_env(env)
    client = GitHubClient(config)
    repository = client.repository()
    repository_id = repository["id"]
    open_update = has_open_update(client, repository_id, config.base_branch)
    if args.check_open:
        write_open_output(env, open_update)
        print("open dependency update pull request found" if open_update else "no open dependency update pull request")
        return 0
    if open_update:
        print("open dependency update pull request already exists; nothing published")
        return 0

    branch_metadata = client.branch(config.base_branch)
    remote_head = branch_metadata["commit"]["sha"]
    git = Git(root)
    if git.current_branch() != config.base_branch:
        raise PublishError("current branch does not match BASE_BRANCH")
    local_head = git.head()
    if local_head != remote_head:
        raise PublishError("local HEAD does not match the current GitHub base branch")
    paths = git.changed_paths()
    if not paths:
        print("no dependency update changes; nothing published")
        return 0
    candidate = verify_files(root, paths)
    branch = f"{BRANCH_PREFIX}{candidate}-{local_head[:8]}"
    validate_branch(branch)
    if client.branch_exists(branch):
        raise PublishError("candidate update branch already exists; refusing to overwrite it")
    git.publish(config, paths, branch)
    client.create_pull(config.base_branch, branch)
    print(f"published dependency update pull request from {branch}")
    return 0


def parse_args(argv: Sequence[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check-open",
        action="store_true",
        help="only report whether an owned automated dependency PR is open",
    )
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    try:
        return run(parse_args(argv if argv is not None else sys.argv[1:]), os.environ, Path.cwd())
    except PublishError as exc:
        print(f"publish-update: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
