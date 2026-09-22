// Package repository materializes explicitly selected Git revisions in a PAW
// workspace without exposing an ambient host checkout or Git credentials.
package repository

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const repositoryStreamTimeout = 3 * time.Minute

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

	streamContext, cancel := context.WithTimeout(context.Background(), repositoryStreamTimeout)
	defer cancel()
	bundle := exec.CommandContext(streamContext, "git", "-C", selected.source, "bundle", "create", "-", selected.ref)
	bundle.WaitDelay = 5 * time.Second
	kubectl := exec.CommandContext(streamContext,
		"kubectl",
		"--context", request.Context,
		"--namespace", "paw-workspace",
		"exec", "--stdin", "workspace-0", "--",
		"sh", "-ceu", materializeScript,
		"paw-repository", request.Name, selected.ref, selected.object, selected.commit,
	)
	kubectl.WaitDelay = 5 * time.Second

	bundle.Stderr = stderr
	kubectl.Stdout = stdout
	kubectl.Stderr = stderr

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create Git bundle pipe: %w", err)
	}
	bundle.Stdout = writer
	kubectl.Stdin = reader

	if err := kubectl.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("start kubectl repository stream: %w", err)
	}
	// Each child now owns its end of the OS pipe. Keeping parent copies open
	// would prevent EOF/SIGPIPE from reaching the other child on early exit.
	_ = reader.Close()
	if err := bundle.Start(); err != nil {
		_ = writer.Close()
		_ = kubectl.Wait()
		return fmt.Errorf("start Git bundle stream: %w", err)
	}
	_ = writer.Close()

	bundleDone := make(chan error, 1)
	go func() {
		bundleErr := bundle.Wait()
		bundleDone <- bundleErr
	}()

	kubectlDone := make(chan error, 1)
	go func() {
		kubectlErr := kubectl.Wait()
		kubectlDone <- kubectlErr
	}()

	kubectlErr := <-kubectlDone
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
bundle_file=""
staging_root=""
trap 'rm -f -- "$bundle_file"; if test -n "$staging_root"; then rm -rf -- "$staging_root"; fi' EXIT
trap 'exit 1' HUP INT TERM
bundle_file="$(mktemp /tmp/paw-repository.XXXXXX)"
staging=""
# Import only the bundle, without recipient templates, hooks, or checkout filters.
GIT_CONFIG_NOSYSTEM=1
GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM GIT_CONFIG_GLOBAL
cat >"$bundle_file"
if test -e "$destination" || test -L "$destination"; then
  echo "repository destination $destination already exists" >&2
  exit 1
fi
bundle_heads="$(git bundle list-heads "$bundle_file")"
if test "$bundle_heads" != "$object $ref"; then
  echo "repository bundle does not match selected ref $ref at $object" >&2
  exit 1
fi
staging_root="$(mktemp -d /workspace/work/.paw-repository.XXXXXX)"
staging="$staging_root/repository"
git -c init.defaultBranch=paw-detached init --quiet --template= "$staging"
git -C "$staging" bundle unbundle "$bundle_file" >/dev/null
test "$(git -C "$staging" rev-parse --verify "$object^{commit}")" = "$commit"
git -C "$staging" checkout --quiet --detach "$commit"
mv --no-clobber -T "$staging" "$destination"
if test -e "$staging"; then
  echo "repository destination $destination appeared during materialization" >&2
  exit 1
fi
`
