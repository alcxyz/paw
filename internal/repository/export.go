package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const exportTimeout = 2 * time.Minute

var fullCommitID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// ExportRequest selects a repository in a workspace and an operator-owned
// destination for its patch.
type ExportRequest struct {
	BaseCommit string
	Context    string
	Name       string
	Output     string
}

// Export retrieves committed and tracked working-tree changes relative to an
// explicit base commit. It never writes repository content to terminal output.
func Export(ctx context.Context, request ExportRequest) error {
	if request.Context == "" {
		return fmt.Errorf("context must not be empty")
	}
	if request.Name == "." || request.Name == ".." || !repositoryName.MatchString(request.Name) {
		return fmt.Errorf("repository name must use 1-64 letters, digits, dots, underscores, or hyphens")
	}
	if !fullCommitID.MatchString(request.BaseCommit) {
		return fmt.Errorf("base commit must be a full 40- or 64-character hexadecimal object ID")
	}
	if !filepath.IsAbs(request.Output) {
		return fmt.Errorf("output path must be absolute")
	}
	if _, err := os.Lstat(request.Output); err == nil {
		return fmt.Errorf("output path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output path: %w", err)
	}

	outputDirectory := filepath.Dir(filepath.Clean(request.Output))
	temporary, err := os.CreateTemp(outputDirectory, ".paw-export-*.patch")
	if err != nil {
		return fmt.Errorf("create private export file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		removeOwnedFile(temporaryName, temporary)
		_ = temporary.Close()
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect private export file: %w", err)
	}

	exportContext, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()
	baseCommit := strings.ToLower(request.BaseCommit)
	kubectl := exec.CommandContext(
		exportContext,
		"kubectl",
		"--context", request.Context,
		"--namespace", "paw-workspace",
		"exec", "workspace-0", "--",
		"sh", "-ceu", exportScript,
		"paw-repository-export", request.Name, baseCommit,
	)
	kubectl.Stdout = temporary
	kubectl.Stderr = io.Discard
	kubectl.WaitDelay = 5 * time.Second
	if err := kubectl.Run(); err != nil {
		if exportContext.Err() != nil {
			return fmt.Errorf("repository export cancelled or timed out")
		}
		return fmt.Errorf("workspace repository export failed; verify workspace availability, the selected base, untracked or ignored content, and submodule state (child diagnostics suppressed)")
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync private export file: %w", err)
	}
	if exportContext.Err() != nil {
		return fmt.Errorf("repository export cancelled or timed out")
	}
	if err := os.Link(temporaryName, request.Output); err != nil {
		if _, inspectErr := os.Lstat(request.Output); inspectErr == nil {
			return fmt.Errorf("output path already exists")
		}
		return fmt.Errorf("publish repository export: %w", err)
	}
	return nil
}

func removeOwnedFile(path string, file *os.File) {
	opened, openedErr := file.Stat()
	current, currentErr := os.Lstat(path)
	if openedErr != nil || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return
	}
	_ = os.Remove(path)
}

const exportScript = `
name="$1"
base="$2"
repository="/workspace/work/$name"

export GIT_ATTR_NOSYSTEM=1
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_COUNT=0
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_DIFF_OPTS=
export GIT_EXTERNAL_DIFF=
export GIT_NO_REPLACE_OBJECTS=1
export GIT_OPTIONAL_LOCKS=0
export GIT_PAGER=cat
unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG GIT_CONFIG_PARAMETERS
unset GIT_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_WORK_TREE

test -d "$repository"
test ! -L "$repository"
test -d "$repository/.git"
test ! -L "$repository/.git"
test "$(git -C "$repository" rev-parse --show-toplevel 2>/dev/null)" = "$repository"
test "$(git -C "$repository" rev-parse --absolute-git-dir 2>/dev/null)" = "$repository/.git"
test "$(git -C "$repository" rev-parse --verify --end-of-options "$base^{commit}" 2>/dev/null)" = "$base"

untracked="$(git -C "$repository" ls-files --others --exclude-standard 2>/dev/null)"
test -z "$untracked"
ignored="$(git -C "$repository" ls-files --others --ignored --exclude-standard 2>/dev/null)"
test -z "$ignored"

submodule_status="$(git -C "$repository" submodule status --recursive 2>/dev/null)"
if test -n "$submodule_status"; then
  printf '%s\n' "$submodule_status" | while IFS= read -r line; do
    case "$line" in
      ' '*) ;;
      *) exit 1 ;;
    esac
  done
  git -C "$repository" submodule foreach --quiet --recursive '
    head="$(git rev-parse HEAD 2>/dev/null)" &&
    status="$(git status --porcelain=v1 --untracked-files=all 2>/dev/null)" &&
    ignored="$(git ls-files --others --ignored --exclude-standard 2>/dev/null)" &&
    test "$head" = "$sha1" && test -z "$status" && test -z "$ignored"
  ' >/dev/null 2>&1
fi

git -C "$repository" \
  -c color.ui=false \
  -c core.attributesFile=/dev/null \
  -c diff.external= \
  -c diff.mnemonicPrefix=false \
  -c diff.noprefix=false \
  diff --binary --full-index --no-color --no-ext-diff --no-textconv \
  --src-prefix=a/ --dst-prefix=b/ "$base" -- 2>/dev/null
`
