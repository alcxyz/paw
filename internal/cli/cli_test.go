package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"version"}, &stdout, &stderr, alwaysAvailable)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.HasPrefix(stdout.String(), "paw ") {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestProfileList(t *testing.T) {
	var stdout bytes.Buffer

	exitCode := run([]string{"profile", "list"}, &stdout, &bytes.Buffer{}, alwaysAvailable)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	for _, expected := range []string{"core", "platform-readonly", "workspace-only", "read-only"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("profile output does not contain %q: %q", expected, stdout.String())
		}
	}
}

func TestDoctorFailsWhenRequiredDependencyIsMissing(t *testing.T) {
	var stdout bytes.Buffer
	lookup := func(name string) (string, error) {
		if name == "git" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}

	exitCode := run([]string{"doctor"}, &stdout, &bytes.Buffer{}, lookup)

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "git") || !strings.Contains(stdout.String(), "missing") {
		t.Fatalf("doctor output did not report missing git: %q", stdout.String())
	}
}

func TestDoctorIgnoresMissingOptionalDependency(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "minikube" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}

	exitCode := run([]string{"doctor"}, &bytes.Buffer{}, &bytes.Buffer{}, lookup)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer

	exitCode := run([]string{"nope"}, &bytes.Buffer{}, &stderr, alwaysAvailable)

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func alwaysAvailable(name string) (string, error) {
	return "/bin/" + name, nil
}
