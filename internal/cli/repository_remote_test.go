package cli

import (
	"bytes"
	"context"
	"io"
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
