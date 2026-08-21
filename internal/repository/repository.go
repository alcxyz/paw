// Package repository materializes explicitly selected Git revisions in a PAW
// workspace without exposing an ambient host checkout or Git credentials.
package repository

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Request selects one local repository ref and its destination in a workspace.
type Request struct {
	Context  string
	Name     string
	Revision string
	Source   string
}

type selection struct {
	commit string
	object string
	ref    string
	source string
}

// Add streams a credential-free Git bundle into the selected workspace. Only
// committed objects reachable from the selected named ref are transferred.
func Add(request Request, stdout, stderr io.Writer) error {
	if request.Context == "" {
		return fmt.Errorf("context must not be empty")
	}
	if request.Name == "." || request.Name == ".." || !repositoryName.MatchString(request.Name) {
		return fmt.Errorf("repository name must use 1-64 letters, digits, dots, underscores, or hyphens")
	}

	selected, err := resolve(request.Source, request.Revision)
	if err != nil {
		return err
	}

	bundle := exec.Command("git", "-C", selected.source, "bundle", "create", "-", selected.ref)
	kubectl := exec.Command(
		"kubectl",
		"--context", request.Context,
		"--namespace", "paw-workspace",
		"exec", "--stdin", "workspace-0", "--",
		"sh", "-ceu", materializeScript,
		"paw-repository", request.Name, selected.ref, selected.object, selected.commit,
	)

	reader, writer := io.Pipe()
	bundle.Stdout = writer
	bundle.Stderr = stderr
	kubectl.Stdin = reader
	kubectl.Stdout = stdout
	kubectl.Stderr = stderr

	if err := kubectl.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("start kubectl repository stream: %w", err)
	}
	if err := bundle.Start(); err != nil {
		_ = writer.CloseWithError(err)
		_ = kubectl.Wait()
		return fmt.Errorf("start Git bundle stream: %w", err)
	}

	bundleDone := make(chan error, 1)
	go func() {
		bundleErr := bundle.Wait()
		_ = writer.CloseWithError(bundleErr)
		bundleDone <- bundleErr
	}()

	kubectlErr := kubectl.Wait()
	_ = reader.Close()
	bundleErr := <-bundleDone
	return finishAdd(request.Name, selected.commit, kubectlErr, bundleErr, stdout)
}

func finishAdd(name, commit string, kubectlErr, bundleErr error, stdout io.Writer) error {
	if kubectlErr != nil {
		return fmt.Errorf("materialize repository in workspace: %w", kubectlErr)
	}
	if bundleErr != nil {
		return fmt.Errorf("create Git bundle: %w", bundleErr)
	}

	fmt.Fprintf(stdout, "repository %s materialized at %s\n", name, commit)
	return nil
}

func resolve(source, revision string) (selection, error) {
	if source == "" {
		return selection{}, fmt.Errorf("repository source must not be empty")
	}
	if revision == "" || revision == "HEAD" || revision == "@" ||
		strings.HasPrefix(revision, "-") {
		return selection{}, fmt.Errorf("repository revision must be an explicit named ref")
	}

	absolute, err := filepath.Abs(source)
	if err != nil {
		return selection{}, fmt.Errorf("resolve repository source: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return selection{}, fmt.Errorf("resolve repository source: %w", err)
	}

	topLevel, err := gitOutput(canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return selection{}, fmt.Errorf("source is not a Git worktree: %w", err)
	}
	canonicalTop, err := filepath.EvalSymlinks(topLevel)
	if err != nil {
		return selection{}, fmt.Errorf("resolve Git worktree root: %w", err)
	}
	if filepath.Clean(canonical) != filepath.Clean(canonicalTop) {
		return selection{}, fmt.Errorf("source must name the Git worktree root exactly")
	}

	ref, err := gitOutput(canonical, "rev-parse", "--symbolic-full-name", revision)
	if err != nil || !allowedRef(ref) {
		return selection{}, fmt.Errorf("revision must resolve to a branch, tag, or remote-tracking ref")
	}
	object, err := gitOutput(
		canonical,
		"rev-parse", "--verify", "--end-of-options", ref+"^{object}",
	)
	if err != nil {
		return selection{}, fmt.Errorf("resolve repository ref object: %w", err)
	}
	commit, err := gitOutput(
		canonical,
		"rev-parse", "--verify", "--end-of-options", ref+"^{commit}",
	)
	if err != nil {
		return selection{}, fmt.Errorf("resolve repository revision: %w", err)
	}

	return selection{source: canonical, ref: ref, object: object, commit: commit}, nil
}

func allowedRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/heads/") ||
		strings.HasPrefix(ref, "refs/tags/") ||
		strings.HasPrefix(ref, "refs/remotes/")
}

func gitOutput(source string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", source}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return strings.TrimSpace(string(output)), nil
}

const materializeScript = `
name="$1"
ref="$2"
object="$3"
commit="$4"
destination="/workspace/work/$name"
if test -e "$destination"; then
  echo "repository destination $destination already exists" >&2
  exit 1
fi
bundle_file="$(mktemp /tmp/paw-repository.XXXXXX)"
staging_root="$(mktemp -d /workspace/work/.paw-repository.XXXXXX)"
staging="$staging_root/repository"
trap 'rm -f -- "$bundle_file"; rm -rf -- "$staging_root"' EXIT
cat >"$bundle_file"
bundle_heads="$(git bundle list-heads "$bundle_file")"
if test "$bundle_heads" != "$object $ref"; then
  echo "repository bundle does not match selected ref $ref at $object" >&2
  exit 1
fi
git -c init.defaultBranch=paw-detached init --quiet "$staging"
git -C "$staging" bundle unbundle "$bundle_file" >/dev/null
test "$(git -C "$staging" rev-parse --verify "$object^{commit}")" = "$commit"
git -C "$staging" checkout --quiet --detach "$commit"
mv --no-clobber -T "$staging" "$destination"
if test -e "$staging"; then
  echo "repository destination $destination appeared during materialization" >&2
  exit 1
fi
`
