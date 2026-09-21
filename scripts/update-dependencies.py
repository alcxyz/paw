#!/usr/bin/env python3
"""Propose bounded updates to PAW-owned T3 and Codex source pins.

This command deliberately edits only dependency metadata in its clean,
disposable CI checkout.  Publishing a branch or pull request is a separate
workflow responsibility.
"""

from __future__ import annotations

import argparse
import base64
import binascii
from dataclasses import dataclass
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tarfile
import tempfile
from typing import Any, Mapping, Sequence
import urllib.error
import urllib.parse
import urllib.request


EXIT_ERROR = 1
EXIT_DEPENDENCY_HASH = 2
EXIT_PRECONDITION = 3

T3_OWNER = "pingdotgg"
T3_REPO = "t3code"
T3_FIELDS = frozenset(
    {"owner", "repo", "rev", "hash", "version", "cargoHash", "pnpmDepsHash"}
)
CODEX_FIELDS = frozenset({"version", "hash", "npmDepsHash"})
VERSION_RE = re.compile(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)\Z")
REVISION_RE = re.compile(r"[0-9a-f]{40}\Z")
SRI_RE = re.compile(r"sha256-([A-Za-z0-9+/]+={0,2})\Z")
HASH_MISMATCH_RE = re.compile(r"\bgot:\s+(sha256-[A-Za-z0-9+/]+={0,2})")
FAKE_HASH = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
T3_RELEASES_URL = (
    "https://api.github.com/repos/pingdotgg/t3code/releases?per_page=100"
)
CODEX_REGISTRY_URL = "https://registry.npmjs.org/@openai%2Fcodex"
CODEX_OPTIONAL_PACKAGES = frozenset(
    {
        "@openai/codex-darwin-arm64",
        "@openai/codex-darwin-x64",
        "@openai/codex-linux-arm64",
        "@openai/codex-linux-x64",
    }
)


class UpdateError(Exception):
    """A normal discovery, validation, or command failure."""


class DependencyHashError(UpdateError):
    """A required generated dependency hash could not be derived."""


class PreconditionError(UpdateError):
    """The updater was not launched in its intentionally narrow environment."""


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: str = ""
    stderr: str = ""


class CommandRunner:
    def run(
        self,
        args: Sequence[str],
        *,
        cwd: Path,
        env: Mapping[str, str] | None = None,
        timeout: int = 1800,
    ) -> CommandResult:
        try:
            result = subprocess.run(
                list(args),
                cwd=cwd,
                check=False,
                capture_output=True,
                text=True,
                env=dict(env) if env is not None else None,
                timeout=timeout,
            )
        except subprocess.TimeoutExpired as error:
            raise UpdateError(f"{args[0]} exceeded its {timeout}-second time limit") from error
        except OSError as error:
            raise UpdateError(f"could not run {args[0]}: {error}") from error
        return CommandResult(result.returncode, result.stdout, result.stderr)


class PublicHTTP:
    """Small unauthenticated client for public release metadata and archives."""

    def _get(self, url: str, *, max_bytes: int) -> bytes:
        request = urllib.request.Request(
            url,
            headers={
                "Accept": "application/vnd.github+json, application/json",
                "User-Agent": "paw-dependency-updater/1",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                declared = response.headers.get("Content-Length")
                if declared is not None and int(declared) > max_bytes:
                    raise UpdateError(f"public response exceeded {max_bytes} bytes")
                content = response.read(max_bytes + 1)
        except (OSError, ValueError, urllib.error.URLError) as error:
            raise UpdateError(f"public upstream request failed for {url}: {error}") from error
        if len(content) > max_bytes:
            raise UpdateError(f"public response exceeded {max_bytes} bytes")
        return content

    def json(self, url: str, *, max_bytes: int = 64 * 1024 * 1024) -> Any:
        try:
            return json.loads(self._get(url, max_bytes=max_bytes))
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise UpdateError(f"upstream returned invalid JSON for {url}") from error

    def bytes(self, url: str, *, max_bytes: int = 256 * 1024 * 1024) -> bytes:
        return self._get(url, max_bytes=max_bytes)


def parse_version(value: Any) -> tuple[int, int, int]:
    if not isinstance(value, str):
        raise UpdateError("release version is not a string")
    match = VERSION_RE.fullmatch(value)
    if match is None:
        raise UpdateError(f"release version is not stable semver: {value!r}")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


def validate_revision(value: Any) -> str:
    if not isinstance(value, str) or REVISION_RE.fullmatch(value) is None:
        raise UpdateError("source revision is not an immutable 40-character commit SHA")
    return value


def validate_sri(value: Any, *, allow_fake: bool = False) -> str:
    if not isinstance(value, str):
        raise UpdateError("hash is not a string")
    match = SRI_RE.fullmatch(value)
    try:
        decoded = base64.b64decode(match.group(1), validate=True) if match else b""
    except (binascii.Error, ValueError):
        decoded = b""
    if len(decoded) != 32:
        raise UpdateError("hash is not a complete sha256 SRI value")
    if not allow_fake and value == FAKE_HASH:
        raise UpdateError("placeholder hash is forbidden in a proposal")
    return value


def load_json_object(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text())
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise UpdateError(f"could not read valid JSON from {path}") from error
    if not isinstance(value, dict):
        raise UpdateError(f"{path} must contain one JSON object")
    return value


def validate_t3_pin(pin: Mapping[str, Any], *, allow_fake: bool = False) -> None:
    if set(pin) != T3_FIELDS:
        raise UpdateError("T3 source metadata has unexpected or missing fields")
    if pin["owner"] != T3_OWNER or pin["repo"] != T3_REPO:
        raise UpdateError(
            "refusing to replace a custom T3 source; the default updater only manages "
            "pingdotgg/t3code"
        )
    parse_version(pin["version"])
    validate_revision(pin["rev"])
    for field in ("hash", "cargoHash", "pnpmDepsHash"):
        validate_sri(pin[field], allow_fake=allow_fake)


def validate_codex_pin(pin: Mapping[str, Any], *, allow_fake: bool = False) -> None:
    if set(pin) != CODEX_FIELDS:
        raise UpdateError("Codex source metadata has unexpected or missing fields")
    parse_version(pin["version"])
    for field in ("hash", "npmDepsHash"):
        validate_sri(pin[field], allow_fake=allow_fake)


def select_stable_t3_release(releases: Any) -> str:
    if not isinstance(releases, list):
        raise UpdateError("T3 release response is not a list")
    candidates: list[tuple[tuple[int, int, int], str]] = []
    for release in releases:
        if not isinstance(release, dict):
            continue
        if release.get("draft") is not False or release.get("prerelease") is not False:
            continue
        tag = release.get("tag_name")
        if not isinstance(tag, str) or not tag.startswith("v"):
            continue
        version = tag[1:]
        try:
            parsed = parse_version(version)
        except UpdateError:
            continue
        candidates.append((parsed, version))
    if not candidates:
        raise UpdateError("no published stable T3 release was found")
    return max(candidates)[1]


def resolve_t3_tag(http: PublicHTTP, version: str) -> str:
    tag = urllib.parse.quote(f"v{version}", safe="")
    value = http.json(
        f"https://api.github.com/repos/{T3_OWNER}/{T3_REPO}/git/ref/tags/{tag}",
        max_bytes=1024 * 1024,
    )
    for _ in range(5):
        if not isinstance(value, dict) or not isinstance(value.get("object"), dict):
            raise UpdateError("T3 tag response has an unexpected shape")
        target = value["object"]
        kind = target.get("type")
        revision = target.get("sha")
        validate_revision(revision)
        if kind == "commit":
            return revision
        if kind != "tag":
            raise UpdateError(f"T3 tag points to unsupported object type {kind!r}")
        value = http.json(
            f"https://api.github.com/repos/{T3_OWNER}/{T3_REPO}/git/tags/{revision}",
            max_bytes=1024 * 1024,
        )
    raise UpdateError("T3 annotated tag nesting exceeded the safety limit")


def stable_codex_version(registry: Any) -> str:
    if not isinstance(registry, dict) or not isinstance(registry.get("dist-tags"), dict):
        raise UpdateError("Codex registry metadata has an unexpected shape")
    version = registry["dist-tags"].get("latest")
    parse_version(version)
    versions = registry.get("versions")
    if not isinstance(versions, dict) or not isinstance(versions.get(version), dict):
        raise UpdateError("Codex stable version is absent from registry version metadata")
    return version


def json_bytes(value: Mapping[str, Any]) -> bytes:
    return (json.dumps(value, indent=2) + "\n").encode()


def atomic_write(path: Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    mode = path.stat().st_mode & 0o777 if path.exists() else 0o644
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "wb") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


class FileTransaction:
    def __init__(self, paths: Sequence[Path]):
        self.paths = tuple(paths)
        self.originals: dict[Path, bytes | None] = {}
        self.committed = False

    def __enter__(self) -> "FileTransaction":
        self.originals = {
            path: path.read_bytes() if path.exists() else None for path in self.paths
        }
        return self

    def commit(self) -> None:
        self.committed = True

    def __exit__(self, exc_type: Any, exc: Any, traceback: Any) -> None:
        if self.committed:
            return
        for path, original in self.originals.items():
            if original is None:
                path.unlink(missing_ok=True)
            else:
                atomic_write(path, original)


def prefetch_source_hash(runner: CommandRunner, root: Path, url: str) -> str:
    result = runner.run(
        ["nix", "store", "prefetch-file", "--unpack", "--json", url], cwd=root
    )
    if result.returncode != 0:
        raise UpdateError("Nix could not prefetch the immutable source archive")
    try:
        output = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise UpdateError("Nix source prefetch returned invalid JSON") from error
    if not isinstance(output, dict):
        raise UpdateError("Nix source prefetch returned an unexpected result")
    return validate_sri(output.get("hash"))


def derive_dependency_hash(
    runner: CommandRunner, root: Path, attribute: str, label: str
) -> str:
    evaluated = runner.run(
        ["nix", "eval", "--raw", f"{attribute}.drvPath"], cwd=root, timeout=300
    )
    derivation = evaluated.stdout.strip()
    if (
        evaluated.returncode != 0
        or not derivation.startswith("/nix/store/")
        or not derivation.endswith(".drv")
        or "\n" in derivation
    ):
        raise DependencyHashError(f"could not evaluate the required {label} derivation")
    result = runner.run(
        ["nix", "build", attribute, "--no-link"], cwd=root, timeout=1800
    )
    if result.returncode == 0:
        raise DependencyHashError(
            f"{label} unexpectedly accepted the placeholder hash; refusing the proposal"
        )
    diagnostic = result.stderr + "\n" + result.stdout
    matches = HASH_MISMATCH_RE.findall(diagnostic)
    target_mismatch = re.search(
        r"hash mismatch in fixed-output derivation\s+['\"]"
        + re.escape(derivation)
        + r"['\"]",
        diagnostic,
    )
    if target_mismatch is None or len(matches) != 1:
        raise DependencyHashError(f"could not derive required {label} from the Nix build")
    try:
        return validate_sri(matches[-1])
    except UpdateError as error:
        raise DependencyHashError(f"Nix returned an invalid {label}") from error


def _safe_package_member(name: str) -> bool:
    path = PurePosixPath(name)
    return (
        not path.is_absolute()
        and ".." not in path.parts
        and bool(path.parts)
        and path.parts[0] == "package"
    )


def extract_codex_archive(content: bytes, destination: Path) -> Path:
    try:
        with tarfile.open(fileobj=io.BytesIO(content), mode="r:gz") as archive:
            members = archive.getmembers()
            if len(members) > 20_000:
                raise UpdateError("Codex archive contains too many entries")
            total_size = sum(member.size for member in members if member.isfile())
            if total_size > 512 * 1024 * 1024:
                raise UpdateError("Codex archive expands beyond the safety limit")
            if any(not _safe_package_member(member.name) for member in members):
                raise UpdateError("Codex archive contains an unsafe path")
            archive.extractall(destination, filter="data")
    except (tarfile.TarError, OSError) as error:
        raise UpdateError("Codex release is not a valid package archive") from error
    package = destination / "package"
    if not package.is_dir():
        raise UpdateError("Codex archive does not contain a package directory")
    return package


def validate_codex_package(package: Path, version: str) -> None:
    if (package / ".npmrc").exists():
        raise UpdateError("Codex archive contains a project npm configuration")
    metadata = load_json_object(package / "package.json")
    if metadata.get("name") != "@openai/codex" or metadata.get("version") != version:
        raise UpdateError("Codex archive identity does not match registry latest")
    executable = (
        metadata.get("bin", {}).get("codex")
        if isinstance(metadata.get("bin"), dict)
        else None
    )
    if (
        not isinstance(executable, str)
        or PurePosixPath(executable).is_absolute()
        or ".." in PurePosixPath(executable).parts
    ):
        raise UpdateError("Codex package does not define a safe bin.codex entry")
    if not (package / executable).is_file():
        raise UpdateError("Codex bin.codex target is missing from the archive")
    optional = metadata.get("optionalDependencies")
    if not isinstance(optional, dict) or not CODEX_OPTIONAL_PACKAGES.issubset(optional):
        raise UpdateError("Codex package is missing required platform payloads")


def validate_codex_lock(content: bytes, version: str) -> None:
    try:
        lock = json.loads(content)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise UpdateError("npm generated an invalid Codex package lock") from error
    packages = lock.get("packages") if isinstance(lock, dict) else None
    root = packages.get("") if isinstance(packages, dict) else None
    if (
        not isinstance(root, dict)
        or lock.get("lockfileVersion") != 3
        or root.get("name") != "@openai/codex"
        or root.get("version") != version
    ):
        raise UpdateError("generated Codex package lock has an unexpected root package")
    optional = root.get("optionalDependencies")
    if not isinstance(optional, dict) or not CODEX_OPTIONAL_PACKAGES.issubset(optional):
        raise UpdateError("generated Codex package lock omits required platform payloads")


class Updater:
    def __init__(self, root: Path, http: PublicHTTP, runner: CommandRunner):
        self.root = root
        self.http = http
        self.runner = runner
        self.t3_path = root / "nix/packages/t3code/source.json"
        self.codex_path = root / "nix/packages/codex-cli/source.json"
        self.codex_lock_path = root / "nix/packages/codex-cli/package-lock.json"

    def _discover_t3(self, current: Mapping[str, Any]) -> tuple[str, str] | None:
        version = select_stable_t3_release(self.http.json(T3_RELEASES_URL))
        revision = resolve_t3_tag(self.http, version)
        current_version = current["version"]
        if current_version == version:
            if current["rev"] != revision:
                raise UpdateError("published T3 release tag moved to a different commit")
            return None
        if VERSION_RE.fullmatch(current_version) and parse_version(version) < parse_version(
            current_version
        ):
            raise UpdateError("refusing to downgrade the stable T3 pin")
        return version, revision

    def _discover_codex(
        self, current: Mapping[str, Any]
    ) -> tuple[str, dict[str, Any]] | None:
        registry = self.http.json(CODEX_REGISTRY_URL)
        version = stable_codex_version(registry)
        if version == current["version"]:
            return None
        if parse_version(version) < parse_version(current["version"]):
            raise UpdateError("refusing to downgrade the stable Codex pin")
        return version, registry["versions"][version]

    def _prepare_t3(self, version: str, revision: str) -> dict[str, Any]:
        url = f"https://github.com/{T3_OWNER}/{T3_REPO}/archive/{revision}.tar.gz"
        source_hash = prefetch_source_hash(self.runner, self.root, url)
        pin = {
            "owner": T3_OWNER,
            "repo": T3_REPO,
            "rev": revision,
            "hash": source_hash,
            "version": version,
            "cargoHash": FAKE_HASH,
            "pnpmDepsHash": FAKE_HASH,
        }
        validate_t3_pin(pin, allow_fake=True)
        atomic_write(self.t3_path, json_bytes(pin))
        pin["cargoHash"] = derive_dependency_hash(
            self.runner,
            self.root,
            ".#t3code-headless.resourceMonitor.cargoDeps",
            "T3 resource monitor cargoHash",
        )
        atomic_write(self.t3_path, json_bytes(pin))
        pin["pnpmDepsHash"] = derive_dependency_hash(
            self.runner,
            self.root,
            ".#t3code-headless.pnpmDeps",
            "T3 pnpmDepsHash",
        )
        validate_t3_pin(pin)
        atomic_write(self.t3_path, json_bytes(pin))
        return pin

    def _prepare_codex(self, version: str, metadata: Mapping[str, Any]) -> dict[str, Any]:
        dist = metadata.get("dist")
        if not isinstance(dist, dict):
            raise UpdateError("Codex version metadata omits distribution information")
        expected_url = f"https://registry.npmjs.org/@openai/codex/-/codex-{version}.tgz"
        if dist.get("tarball") != expected_url:
            raise UpdateError("Codex registry tarball URL does not match the pinned recipe")
        archive = self.http.bytes(expected_url)
        source_hash = prefetch_source_hash(self.runner, self.root, expected_url)
        with tempfile.TemporaryDirectory(prefix="paw-codex-update-") as directory:
            temporary_root = Path(directory)
            package = extract_codex_archive(archive, temporary_root)
            validate_codex_package(package, version)
            # Regenerate from package.json instead of trusting dependency state
            # that an upstream archive may happen to carry.
            (package / "package-lock.json").unlink(missing_ok=True)
            (package / "npm-shrinkwrap.json").unlink(missing_ok=True)
            npm_home = temporary_root / "home"
            npm_cache = temporary_root / "cache"
            npm_home.mkdir()
            npm_cache.mkdir()
            npm_environment = {
                "HOME": str(npm_home),
                "PATH": os.environ.get("PATH", ""),
                "LANG": os.environ.get("LANG", "C.UTF-8"),
                "LC_ALL": os.environ.get("LC_ALL", "C.UTF-8"),
                "npm_config_userconfig": str(temporary_root / "empty-npmrc"),
                "npm_config_globalconfig": str(temporary_root / "empty-global-npmrc"),
                "npm_config_cache": str(npm_cache),
                "npm_config_registry": "https://registry.npmjs.org/",
                "npm_config_audit": "false",
                "npm_config_fund": "false",
                "npm_config_ignore_scripts": "true",
            }
            result = self.runner.run(
                [
                    "nix",
                    "shell",
                    "--no-write-lock-file",
                    "--inputs-from",
                    str(self.root),
                    "nixpkgs#nodejs_24",
                    "--command",
                    "npm",
                    "install",
                    "--package-lock-only",
                    "--ignore-scripts",
                    "--no-audit",
                    "--no-fund",
                ],
                cwd=package,
                env=npm_environment,
                timeout=600,
            )
            if result.returncode != 0:
                raise UpdateError("npm could not generate the Codex package lock")
            try:
                lock_content = (package / "package-lock.json").read_bytes()
            except OSError as error:
                raise UpdateError("npm did not generate the Codex package lock") from error
        validate_codex_lock(lock_content, version)
        pin = {"version": version, "hash": source_hash, "npmDepsHash": FAKE_HASH}
        validate_codex_pin(pin, allow_fake=True)
        atomic_write(self.codex_path, json_bytes(pin))
        atomic_write(self.codex_lock_path, lock_content)
        pin["npmDepsHash"] = derive_dependency_hash(
            self.runner, self.root, ".#codex-cli.npmDeps", "Codex npmDepsHash"
        )
        validate_codex_pin(pin)
        atomic_write(self.codex_path, json_bytes(pin))
        return pin

    def propose(self, component: str = "all") -> list[str]:
        current_t3 = load_json_object(self.t3_path)
        current_codex = load_json_object(self.codex_path)
        validate_t3_pin(current_t3)
        validate_codex_pin(current_codex)

        t3_target = (
            self._discover_t3(current_t3)
            if component in ("all", "t3code")
            else None
        )
        codex_target = (
            self._discover_codex(current_codex) if component in ("all", "codex") else None
        )
        if t3_target is None and codex_target is None:
            return []

        changed: list[str] = []
        paths = (self.t3_path, self.codex_path, self.codex_lock_path)
        with FileTransaction(paths) as transaction:
            if t3_target is not None:
                self._prepare_t3(*t3_target)
                changed.append("t3code")
            if codex_target is not None:
                self._prepare_codex(*codex_target)
                changed.append("codex")
            # Re-read final files so an incomplete or placeholder proposal can
            # never be accepted merely because the in-memory object was valid.
            validate_t3_pin(load_json_object(self.t3_path))
            validate_codex_pin(load_json_object(self.codex_path))
            if codex_target is not None:
                validate_codex_lock(self.codex_lock_path.read_bytes(), codex_target[0])
            transaction.commit()
        return changed

    def check(self, component: str = "all") -> dict[str, dict[str, Any]]:
        """Discover candidates without invoking commands or changing files."""
        report: dict[str, dict[str, Any]] = {}
        if component in ("all", "t3code"):
            current_t3 = load_json_object(self.t3_path)
            validate_t3_pin(current_t3)
            target = self._discover_t3(current_t3)
            report["t3code"] = {
                "current": current_t3["version"],
                "target": target[0] if target else current_t3["version"],
                "revision": target[1] if target else current_t3["rev"],
                "updateAvailable": target is not None,
            }
        if component in ("all", "codex"):
            current_codex = load_json_object(self.codex_path)
            validate_codex_pin(current_codex)
            target = self._discover_codex(current_codex)
            report["codex"] = {
                "current": current_codex["version"],
                "target": target[0] if target else current_codex["version"],
                "updateAvailable": target is not None,
            }
        return report


def enforce_ci_checkout(
    root: Path, runner: CommandRunner, env: Mapping[str, str]
) -> None:
    if (
        env.get("CI", "").lower() != "true"
        or env.get("PAW_DISPOSABLE_UPDATE_CHECKOUT") != "1"
    ):
        raise PreconditionError(
            "dependency updates require CI=true and PAW_DISPOSABLE_UPDATE_CHECKOUT=1"
        )
    top = runner.run(
        ["git", "rev-parse", "--show-toplevel"], cwd=root, timeout=30
    )
    if top.returncode != 0 or Path(top.stdout.strip()).resolve() != root.resolve():
        raise PreconditionError("updater root is not the current Git checkout root")
    status = runner.run(
        ["git", "status", "--porcelain=v1", "--untracked-files=all"],
        cwd=root,
        timeout=30,
    )
    if status.returncode != 0:
        raise PreconditionError("could not verify that the update checkout is clean")
    if status.stdout:
        raise PreconditionError("dependency updater requires a clean disposable checkout")


def argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Propose stable T3 and Codex pin updates in a disposable CI checkout.",
        epilog=(
            "This command does not commit, push, open pull requests, merge, publish, or deploy. "
            "Claude Code and OpenCode follow explicit flake.lock/nixpkgs updates and are not "
            "managed here. Exit 2 specifically means required dependency-hash derivation failed."
        ),
    )
    parser.add_argument(
        "--component", choices=("all", "t3code", "codex"), default="all"
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="report stable candidates without file writes, source fetches, or Nix builds",
    )
    return parser


def main(
    argv: Sequence[str] | None = None,
    *,
    root: Path | None = None,
    env: Mapping[str, str] | None = None,
    runner: CommandRunner | None = None,
    http: PublicHTTP | None = None,
) -> int:
    arguments = argument_parser().parse_args(argv)
    repository = root or Path(__file__).resolve().parents[1]
    command_runner = runner or CommandRunner()
    try:
        updater = Updater(repository, http or PublicHTTP(), command_runner)
        if arguments.check:
            print(json.dumps(updater.check(arguments.component), sort_keys=True))
            return 0
        enforce_ci_checkout(repository, command_runner, env or os.environ)
        changed = updater.propose(arguments.component)
    except DependencyHashError as error:
        print(f"dependency hash failure: {error}", file=sys.stderr)
        return EXIT_DEPENDENCY_HASH
    except PreconditionError as error:
        print(f"precondition failure: {error}", file=sys.stderr)
        return EXIT_PRECONDITION
    except UpdateError as error:
        print(f"update failure: {error}", file=sys.stderr)
        return EXIT_ERROR
    if not changed:
        print("Dependency pins are already current; no proposal generated.")
    else:
        print("Prepared dependency update proposal: " + ", ".join(changed))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
