package repository

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSelectsNamedCommittedRef(t *testing.T) {
	repositoryPath := newTestRepository(t)
	commit := gitTestOutput(t, repositoryPath, "rev-parse", "HEAD")

	selected, err := resolve(repositoryPath, "HEAD")
	if err != nil {
		t.Fatalf("resolve repository: %v", err)
	}
	if selected.source != repositoryPath {
		t.Fatalf("unexpected source %q", selected.source)
	}
	if selected.ref != "refs/heads/main" {
		t.Fatalf("unexpected ref %q", selected.ref)
	}
	if selected.commit != commit {
		t.Fatalf("expected commit %q, got %q", commit, selected.commit)
	}
}

func TestResolveRejectsSubdirectoryAndRawCommit(t *testing.T) {
	repositoryPath := newTestRepository(t)
	child := filepath.Join(repositoryPath, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(child, "HEAD"); err == nil || !strings.Contains(err.Error(), "root exactly") {
		t.Fatalf("expected exact-root rejection, got %v", err)
	}

	commit := gitTestOutput(t, repositoryPath, "rev-parse", "HEAD")
	if _, err := resolve(repositoryPath, commit); err == nil || !strings.Contains(err.Error(), "branch, tag") {
		t.Fatalf("expected raw-commit rejection, got %v", err)
	}
}

func TestAddRejectsUnsafeDestinationNameBeforeExecution(t *testing.T) {
	err := Add(Request{Context: "paw-local", Name: "../ambient"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "repository name") {
		t.Fatalf("expected destination-name rejection, got %v", err)
	}
}

func TestMaterializationRemovesRemoteAndUsesAtomicDestination(t *testing.T) {
	for _, required := range []string{
		"mktemp -d /workspace/work/.paw-repository.",
		"checkout --quiet --detach",
		"remote remove origin",
		"mv \"$staging\" \"$destination\"",
	} {
		if !strings.Contains(materializeScript, required) {
			t.Fatalf("materialization script is missing %q", required)
		}
	}
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	repositoryPath := t.TempDir()
	gitTestRun(t, repositoryPath, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repositoryPath, "add", "README.md")
	gitTestRun(
		t,
		repositoryPath,
		"-c", "user.name=PAW test",
		"-c", "user.email=paw-test.invalid",
		"commit", "--message=initial",
	)
	canonical, err := filepath.EvalSymlinks(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func gitTestRun(t *testing.T, repositoryPath string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func gitTestOutput(t *testing.T, repositoryPath string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
