package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func linkTestDependencies(t *testing.T, allow string, recorded *[][]string) dependencies {
	t.Helper()
	return workspaceTestDependencies(func(name string, args []string, stdout, stderr io.Writer) error {
		if name != "git" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		*recorded = append(*recorded, args)
		if len(args) >= 2 && args[0] == "config" {
			_, _ = io.WriteString(stdout, allow+"\n")
		}
		return nil
	})
}

func TestWorkspaceRepositoryLinkAddsExtRemote(t *testing.T) {
	var recorded [][]string
	var stdout bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "repository", "link", "--adapter", "minikube", "--context", "paw-smoke", "--name", "paw"},
		&stdout, &bytes.Buffer{}, linkTestDependencies(t, "user", &recorded),
	)
	if exitCode != 0 {
		t.Fatalf("link failed: exit=%d", exitCode)
	}
	last := recorded[len(recorded)-1]
	want := []string{"remote", "add", "paw", "ext::paw workspace repository remote --adapter minikube --context paw-smoke --name paw --service %S"}
	if strings.Join(last, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected git remote add: %v", last)
	}
	if !strings.Contains(stdout.String(), "git fetch paw") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestWorkspaceRepositoryLinkHonoursRemoteAndCommand(t *testing.T) {
	var recorded [][]string
	exitCode := runWithDependencies(
		[]string{"workspace", "repository", "link", "--adapter", "kubernetes", "--context", "funhouse", "--name", "paw", "--remote", "workspace", "--command", "/nix/store/x/bin/paw"},
		&bytes.Buffer{}, &bytes.Buffer{}, linkTestDependencies(t, "always", &recorded),
	)
	last := recorded[len(recorded)-1]
	if exitCode != 0 || last[2] != "workspace" || !strings.HasPrefix(last[3], "ext::/nix/store/x/bin/paw workspace repository remote --adapter kubernetes --context funhouse") {
		t.Fatalf("unexpected result: exit=%d args=%v", exitCode, last)
	}
}

func TestWorkspaceRepositoryLinkRefusesWithoutExtAllowed(t *testing.T) {
	var recorded [][]string
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "repository", "link", "--adapter", "minikube", "--context", "paw-smoke", "--name", "paw"},
		&bytes.Buffer{}, &stderr, linkTestDependencies(t, "", &recorded),
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "protocol.ext.allow user") {
		t.Fatalf("expected ext refusal, got exit=%d stderr=%q", exitCode, stderr.String())
	}
	for _, args := range recorded {
		if args[0] == "remote" {
			t.Fatalf("remote must not be added when ext is disabled: %v", recorded)
		}
	}
}

func TestWorkspaceRepositoryLinkValidation(t *testing.T) {
	for name, args := range map[string][]string{
		"missing name":   {"--adapter", "minikube", "--context", "x"},
		"bad name":       {"--adapter", "minikube", "--context", "x", "--name", "../x"},
		"bad remote":     {"--adapter", "minikube", "--context", "x", "--name", "paw", "--remote", "a b"},
		"quoted context": {"--adapter", "minikube", "--context", "x'y", "--name", "paw"},
		"unknown option": {"--adapter", "minikube", "--context", "x", "--name", "paw", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			exitCode := runWithDependencies(append([]string{"workspace", "repository", "link"}, args...), &bytes.Buffer{}, &stderr, workspaceTestDependencies(nil))
			if exitCode != 2 {
				t.Fatalf("expected usage error, got exit=%d stderr=%q", exitCode, stderr.String())
			}
		})
	}
}
