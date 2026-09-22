package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
)

func sessionTestDependencies(t *testing.T, recordedProvider string, interactive *[][]string) dependencies {
	t.Helper()
	deps := workspaceTestDependencies(func(name string, args []string, stdout, stderr io.Writer) error {
		if name != "kubectl" || !slices.Contains(args, "statefulset/workspace") {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		statefulSet := statefulSetResource{}
		statefulSet.Metadata.Name = "workspace"
		statefulSet.Metadata.Annotations = map[string]string{"paw.alc.xyz/provider": recordedProvider}
		return json.NewEncoder(stdout).Encode(statefulSet)
	})
	deps.runInteractive = func(name string, args []string) error {
		if name != "kubectl" {
			t.Fatalf("unexpected interactive command %s", name)
		}
		*interactive = append(*interactive, args)
		return nil
	}
	return deps
}

func TestWorkspaceLoginRunsProviderLoginInsidePod(t *testing.T) {
	var interactive [][]string
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "login", "--adapter", "minikube", "--context", "paw-local", "--provider", "codex"},
		&stdout, &stderr, sessionTestDependencies(t, "codex", &interactive),
	)
	if exitCode != 0 {
		t.Fatalf("login failed: exit=%d stderr=%q", exitCode, stderr.String())
	}
	expected := []string{
		"--context", "paw-local", "--namespace", "paw-workspace",
		"exec", "--stdin", "--tty", "workspace-0", "--container", "t3", "--",
		"codex", "login", "--device-auth",
	}
	if len(interactive) != 1 || !slices.Equal(interactive[0], expected) {
		t.Fatalf("unexpected interactive commands: %v", interactive)
	}
	if !strings.Contains(stdout.String(), "session volume") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestWorkspaceLogoutRunsProviderLogoutInsidePod(t *testing.T) {
	var interactive [][]string
	exitCode := runWithDependencies(
		[]string{"workspace", "logout", "--adapter", "kubernetes", "--context", "paw-remote", "--provider", "claude-code"},
		&bytes.Buffer{}, &bytes.Buffer{}, sessionTestDependencies(t, "claude-code", &interactive),
	)
	if exitCode != 0 {
		t.Fatalf("logout failed: exit=%d", exitCode)
	}
	if len(interactive) != 1 || !strings.HasSuffix(strings.Join(interactive[0], " "), "-- claude auth logout") {
		t.Fatalf("unexpected interactive commands: %v", interactive)
	}
}

func TestWorkspaceLoginRefusesMismatchedProvider(t *testing.T) {
	var interactive [][]string
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "login", "--adapter", "minikube", "--context", "paw-local", "--provider", "codex"},
		&bytes.Buffer{}, &stderr, sessionTestDependencies(t, "claude-code", &interactive),
	)
	if exitCode != 1 || len(interactive) != 0 || !strings.Contains(stderr.String(), `records provider "claude-code"`) {
		t.Fatalf("expected a provider mismatch refusal, got exit=%d stderr=%q commands=%v", exitCode, stderr.String(), interactive)
	}
}

func TestWorkspaceLoginArgumentValidation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{name: "missing provider", args: []string{"workspace", "login", "--adapter", "minikube", "--context", "x"}, expected: "require --provider"},
		{name: "unreviewed provider", args: []string{"workspace", "login", "--adapter", "minikube", "--context", "x", "--provider", "opencode"}, expected: "no reviewed login flow"},
		{name: "provider none", args: []string{"workspace", "logout", "--adapter", "minikube", "--context", "x", "--provider", "none"}, expected: "no reviewed login flow"},
		{name: "provider on inspect", args: []string{"workspace", "inspect", "--adapter", "minikube", "--context", "x", "--provider", "codex"}, expected: "does not accept --provider"},
		{name: "profile on login", args: []string{"workspace", "login", "--adapter", "minikube", "--context", "x", "--provider", "codex", "--profile", "core"}, expected: "does not accept --profile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			exitCode := runWithDependencies(test.args, &bytes.Buffer{}, &stderr, workspaceTestDependencies(nil))
			if exitCode != 2 || !strings.Contains(stderr.String(), test.expected) {
				t.Fatalf("expected usage error %q, got exit=%d stderr=%q", test.expected, exitCode, stderr.String())
			}
		})
	}
}
