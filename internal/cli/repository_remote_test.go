package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceRepositoryRemoteTunnelsGitService(t *testing.T) {
	var recorded []string
	deps := workspaceTestDependencies(nil)
	deps.runInput = func(_ context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		if name != "kubectl" {
			t.Fatalf("unexpected command %s", name)
		}
		recorded = args
		return nil
	}
	var stdout bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "repository", "remote", "--adapter", "minikube", "--context", "paw-local", "--name", "paw", "--service", "git-upload-pack"},
		&stdout, &bytes.Buffer{}, deps,
	)
	expected := []string{
		"--context", "paw-local", "--namespace", "paw-workspace",
		"exec", "--stdin", "--quiet", "workspace-0", "--container", "t3", "--",
		"git-upload-pack", "/workspace/work/paw",
	}
	if exitCode != 0 || !slices.Equal(recorded, expected) {
		t.Fatalf("unexpected transport: exit=%d args=%v", exitCode, recorded)
	}
	if stdout.Len() != 0 {
		t.Fatalf("transport wrote to stdout outside the Git stream: %q", stdout.String())
	}
}

func TestWorkspaceRepositoryRemoteValidation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{name: "missing service", args: []string{"--adapter", "minikube", "--context", "x", "--name", "paw"}, expected: "usage:"},
		{name: "unknown service", args: []string{"--adapter", "minikube", "--context", "x", "--name", "paw", "--service", "git-shell"}, expected: "git-upload-pack or git-receive-pack"},
		{name: "bad name", args: []string{"--adapter", "minikube", "--context", "x", "--name", "../etc", "--service", "git-upload-pack"}, expected: "repository name"},
		{name: "bad adapter", args: []string{"--adapter", "docker", "--context", "x", "--name", "paw", "--service", "git-upload-pack"}, expected: "unsupported adapter"},
		{name: "unknown option", args: []string{"--adapter", "minikube", "--context", "x", "--name", "paw", "--service", "git-upload-pack", "--tty"}, expected: "unknown repository option"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			deps := workspaceTestDependencies(nil)
			deps.runInput = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("transport must not run on invalid arguments")
				return nil
			}
			exitCode := runWithDependencies(append([]string{"workspace", "repository", "remote"}, test.args...), &bytes.Buffer{}, &stderr, deps)
			if exitCode != 2 || !strings.Contains(stderr.String(), test.expected) {
				t.Fatalf("expected usage error %q, got exit=%d stderr=%q", test.expected, exitCode, stderr.String())
			}
		})
	}
}

func TestReceiveTransportRefusesCurrentBranchWithWritableSettings(t *testing.T) {
	// Capture the actual in-pod service command, then exercise it against local
	// synthetic repositories through Git's real push protocol.
	var serviceArgs []string
	deps := workspaceTestDependencies(nil)
	deps.runInput = func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
		split := slices.Index(args, "--")
		serviceArgs = slices.Clone(args[split+1 : len(args)-1])
		return nil
	}
	if exit := runWithDependencies([]string{"workspace", "repository", "remote", "--adapter", "minikube", "--context", "fixture", "--name", "fixture", "--service", "git-receive-pack"}, io.Discard, io.Discard, deps); exit != 0 {
		t.Fatalf("transport exit %d", exit)
	}
	for _, fixture := range []struct{ name, current, deletion, bare string }{
		{"ignore", "ignore", "refuse", "false"},
		{"warn", "warn", "refuse", "false"},
		{"updateInstead", "updateInstead", "refuse", "false"},
		{"delete ignore", "refuse", "ignore", "false"},
		{"delete warn", "refuse", "warn", "false"},
		{"bare", "ignore", "ignore", "true"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			git := func(dir string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			git(root, "init", "--initial-branch=work", source)
			git(source, "config", "user.name", "Fixture")
			git(source, "config", "user.email", "fixture@example.invalid")
			if err := os.WriteFile(filepath.Join(source, "file"), []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(source, "add", "file")
			git(source, "commit", "-m", "fixture before")
			before := git(source, "rev-parse", "HEAD")
			git(root, "clone", source, target)
			git(target, "config", "receive.denyCurrentBranch", fixture.current)
			git(target, "config", "receive.denyDeleteCurrent", fixture.deletion)
			git(target, "config", "core.bare", fixture.bare)
			if err := os.WriteFile(filepath.Join(source, "file"), []byte("after\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(source, "commit", "-am", "fixture after")
			after := git(source, "rev-parse", "HEAD")
			cmd := exec.Command("git", "-C", source, "push", "--receive-pack="+strings.Join(serviceArgs, " "), target, "HEAD:refs/heads/work")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "branch is currently checked out") {
				t.Fatalf("current branch must be refused: %v: %s", err, out)
			}
			cmd = exec.Command("git", "-C", source, "push", "--receive-pack="+strings.Join(serviceArgs, " "), target, ":refs/heads/work")
			cmd.Env = env
			out, err = cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "branch is currently checked out") {
				t.Fatalf("current branch deletion must be refused: %v: %s", err, out)
			}
			if got := git(target, "rev-parse", "HEAD"); got != before {
				t.Fatalf("HEAD moved: %s", got)
			}
			data, err := os.ReadFile(filepath.Join(target, "file"))
			if err != nil || string(data) != "before\n" {
				t.Fatalf("worktree changed: %q, %v", data, err)
			}
			if git(target, "-c", "core.bare=false", "--work-tree="+target, "status", "--porcelain") != "" {
				t.Fatal("index or worktree changed")
			}
			git(source, "push", "--receive-pack="+strings.Join(serviceArgs, " "), target, "HEAD:refs/heads/incoming")
			if got := git(target, "rev-parse", "refs/heads/incoming"); got != after {
				t.Fatalf("incoming branch = %s", got)
			}
			git(target, "fetch", source, "work:refs/heads/fetched")
			if got := git(target, "rev-parse", "refs/heads/fetched"); got != after {
				t.Fatalf("fetch lost commit: %s", got)
			}
		})
	}
}
