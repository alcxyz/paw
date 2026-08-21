package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"

	deployment "git.alc.xyz/alcxyz/paw/deploy"
	"git.alc.xyz/alcxyz/paw/internal/repository"
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

func TestProfileShowExposesEffectiveAuthority(t *testing.T) {
	var stdout bytes.Buffer

	exitCode := run(
		[]string{"profile", "show", "platform-readonly"},
		&stdout,
		&bytes.Buffer{},
		alwaysAvailable,
	)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	for _, expected := range []string{
		"read-only",
		"EXTERNAL MUTATION",
		"denied",
		"PLATFORM IDENTITY",
		"required",
		"terraform-apply",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("profile output does not contain %q: %q", expected, stdout.String())
		}
	}
}

func TestProfileShowJSON(t *testing.T) {
	var stdout bytes.Buffer

	exitCode := run(
		[]string{"profile", "show", "core", "--json"},
		&stdout,
		&bytes.Buffer{},
		alwaysAvailable,
	)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	for _, expected := range []string{
		`"authorityCeiling": "workspace-only"`,
		`"externalMutation": false`,
		`"remoteGitPush": false`,
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("JSON output does not contain %q: %q", expected, stdout.String())
		}
	}
}

func TestProfileShowRejectsUnknownProfile(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := run(
		[]string{"profile", "show", "nope"},
		&bytes.Buffer{},
		&stderr,
		alwaysAvailable,
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "unknown profile") {
		t.Fatalf("unexpected result: exit=%d stderr=%q", exitCode, stderr.String())
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

func TestWorkspaceRender(t *testing.T) {
	var stdout bytes.Buffer
	var commandName string
	var commandArgs []string
	deps := workspaceTestDependencies(func(name string, args []string, output, _ io.Writer) error {
		commandName = name
		commandArgs = slices.Clone(args)
		_, _ = io.WriteString(output, "rendered")
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "render",
			"--adapter", "minikube",
			"--profile", "core",
			"--provider", "codex",
		},
		&stdout,
		&bytes.Buffer{},
		deps,
	)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if commandName != "kubectl" || !slices.Equal(commandArgs, []string{"kustomize", "/manifests/minikube"}) {
		t.Fatalf("unexpected command: %s %v", commandName, commandArgs)
	}
	if stdout.String() != "rendered" {
		t.Fatalf("unexpected output %q", stdout.String())
	}
}

func TestWorkspaceCreateRequiresExplicitContext(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "create",
			"--adapter", "minikube",
			"--profile", "core",
			"--provider", "none",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "requires --context") {
		t.Fatalf("expected context usage error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceCreateUsesSelectedContext(t *testing.T) {
	var commandArgs []string
	var selection deployment.Selection
	deps := workspaceTestDependencies(func(_ string, args []string, _, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		return nil
	})
	deps.materialize = func(_ string, actual deployment.Selection) (string, func(), error) {
		selection = actual
		return "/manifests/minikube", func() {}, nil
	}

	exitCode := runWithDependencies(
		[]string{
			"workspace", "create",
			"--adapter", "minikube",
			"--context", "minikube",
			"--profile", "platform-readonly",
			"--provider", "opencode",
		},
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)

	expected := []string{"--context", "minikube", "apply", "-k", "/manifests/minikube"}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected create result: exit=%d args=%v", exitCode, commandArgs)
	}
	if selection != (deployment.Selection{Profile: "platform-readonly", Provider: "opencode"}) {
		t.Fatalf("unexpected selection: %#v", selection)
	}
}

func TestWorkspaceRenderRequiresExplicitSelection(t *testing.T) {
	for _, args := range [][]string{
		{"workspace", "render", "--adapter", "minikube", "--provider", "none"},
		{"workspace", "render", "--adapter", "minikube", "--profile", "core"},
	} {
		var stderr bytes.Buffer
		exitCode := runWithDependencies(
			args,
			&bytes.Buffer{},
			&stderr,
			workspaceTestDependencies(nil),
		)
		if exitCode != 2 || !strings.Contains(stderr.String(), "require") {
			t.Fatalf("expected selection usage error, got %d and %q", exitCode, stderr.String())
		}
	}
}

func TestWorkspaceRejectsUnknownSelection(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "render",
			"--adapter", "minikube",
			"--profile", "production",
			"--provider", "codex",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "unsupported profile/provider") {
		t.Fatalf("expected selection usage error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceDestroyRequiresExplicitStateDeletion(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"workspace", "destroy", "--adapter", "minikube", "--context", "minikube"},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "requires --delete-state") {
		t.Fatalf("expected state deletion usage error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceDestroyDeletesExactKustomization(t *testing.T) {
	var commandArgs []string
	deps := workspaceTestDependencies(func(_ string, args []string, _, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "destroy",
			"--adapter", "minikube",
			"--context", "minikube",
			"--delete-state",
		},
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)

	expected := []string{
		"--context", "minikube",
		"delete", "-k", "/manifests/minikube",
		"--ignore-not-found=true",
	}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected destroy result: exit=%d args=%v", exitCode, commandArgs)
	}
}

func TestWorkspaceInspectUsesExactResources(t *testing.T) {
	var commandArgs []string
	deps := workspaceTestDependencies(func(_ string, args []string, _, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "inspect",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--json",
		},
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)

	expected := []string{
		"--context", "paw-local",
		"--namespace", "paw-workspace",
		"get",
		"statefulset/workspace",
		"pod/workspace-0",
		"persistentvolumeclaim/workspace-state",
		"service/t3",
		"--output", "json",
	}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected inspect result: exit=%d args=%v", exitCode, commandArgs)
	}
}

func TestWorkspaceConnectIsLoopbackOnly(t *testing.T) {
	var commandArgs []string
	deps := workspaceTestDependencies(func(_ string, args []string, _, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "connect",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--local-port", "43773",
		},
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)

	expected := []string{
		"--context", "paw-local",
		"--namespace", "paw-workspace",
		"port-forward",
		"--address", "127.0.0.1",
		"service/t3",
		"43773:3773",
	}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected connect result: exit=%d args=%v", exitCode, commandArgs)
	}
}

func TestWorkspacePairMintsShortLivedCredential(t *testing.T) {
	var stdout bytes.Buffer
	var commandArgs []string
	deps := workspaceTestDependencies(func(_ string, args []string, output, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		_, _ = io.WriteString(output, "pairing response")
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "pair",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--ttl", "15m",
			"--label", "phone-browser",
			"--json",
		},
		&stdout,
		&bytes.Buffer{},
		deps,
	)

	expected := []string{
		"--context", "paw-local",
		"--namespace", "paw-workspace",
		"exec", "workspace-0", "--",
		"t3", "auth", "pairing", "create",
		"--base-dir", "/workspace/state/t3",
		"--base-url", "http://127.0.0.1:3773",
		"--ttl", "15m",
		"--label", "phone-browser",
		"--json",
	}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected pair result: exit=%d args=%v", exitCode, commandArgs)
	}
	if stdout.String() != "pairing response" {
		t.Fatalf("pairing output was not returned to the operator: %q", stdout.String())
	}
}

func TestWorkspacePairRejectsLongLivedCredential(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "pair",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--ttl", "24h",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "no longer than 1h") {
		t.Fatalf("expected bounded TTL error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceRevokeUsesPairingIdentifier(t *testing.T) {
	var commandArgs []string
	deps := workspaceTestDependencies(func(_ string, args []string, _, _ io.Writer) error {
		commandArgs = slices.Clone(args)
		return nil
	})

	exitCode := runWithDependencies(
		[]string{
			"workspace", "revoke",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--pairing-id", "pairing-test-id",
		},
		&bytes.Buffer{},
		&bytes.Buffer{},
		deps,
	)

	expected := []string{
		"--context", "paw-local",
		"--namespace", "paw-workspace",
		"exec", "workspace-0", "--",
		"t3", "auth", "pairing", "revoke",
		"--base-dir", "/workspace/state/t3",
		"pairing-test-id",
	}
	if exitCode != 0 || !slices.Equal(commandArgs, expected) {
		t.Fatalf("unexpected revoke result: exit=%d args=%v", exitCode, commandArgs)
	}
}

func TestWorkspaceRevokeRequiresPairingIdentifier(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "revoke",
			"--adapter", "minikube",
			"--context", "paw-local",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "requires --pairing-id") {
		t.Fatalf("expected pairing identifier error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceRejectsOperatorFlagsOnWrongOperation(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "connect",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--json",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "--json is only valid") {
		t.Fatalf("expected scoped option error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceOptionsDoNotConsumeAnotherFlagAsAValue(t *testing.T) {
	for _, option := range []string{
		"--adapter",
		"--context",
		"--label",
		"--local-port",
		"--pairing-id",
		"--profile",
		"--provider",
		"--ttl",
	} {
		t.Run(option, func(t *testing.T) {
			_, err := parseWorkspaceOptions([]string{option, "--json"})
			if err == nil || !strings.Contains(err.Error(), option+" requires a value") {
				t.Fatalf("expected missing value error for %s, got %v", option, err)
			}
		})
	}
}

func TestExecuteCommandForwardsTerminationSignal(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	defer stdoutReader.Close()

	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		if scanner.Scan() && scanner.Text() == "ready" {
			close(ready)
			signals <- syscall.SIGTERM
		}
	}()

	err := executeCommandWithSignals(
		"sh",
		[]string{"-c", "trap 'exit 42' TERM; echo ready; while :; do :; done"},
		stdoutWriter,
		io.Discard,
		signals,
	)
	_ = stdoutWriter.Close()
	<-ready

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 42 {
		t.Fatalf("expected forwarded SIGTERM to trigger exit 42, got %v", err)
	}
}

func TestWorkspaceRepositoryAddUsesExplicitSelection(t *testing.T) {
	var actual repository.Request
	deps := workspaceTestDependencies(nil)
	deps.addRepo = func(request repository.Request, output, _ io.Writer) error {
		actual = request
		_, _ = io.WriteString(output, "materialized")
		return nil
	}
	var stdout bytes.Buffer

	exitCode := runWithDependencies(
		[]string{
			"workspace", "repository", "add",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--source", "/selected/repository",
			"--revision", "refs/heads/main",
			"--name", "platform",
		},
		&stdout,
		&bytes.Buffer{},
		deps,
	)

	expected := repository.Request{
		Context:  "paw-local",
		Name:     "platform",
		Revision: "refs/heads/main",
		Source:   "/selected/repository",
	}
	if exitCode != 0 || actual != expected {
		t.Fatalf("unexpected repository request: exit=%d request=%#v", exitCode, actual)
	}
	if stdout.String() != "materialized" {
		t.Fatalf("repository output was not returned: %q", stdout.String())
	}
}

func TestWorkspaceRepositoryAddRequiresCompleteSelection(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{
			"workspace", "repository", "add",
			"--adapter", "minikube",
			"--context", "paw-local",
			"--source", "/selected/repository",
			"--name", "platform",
		},
		&bytes.Buffer{},
		&stderr,
		workspaceTestDependencies(nil),
	)

	if exitCode != 2 || !strings.Contains(stderr.String(), "--revision REF") {
		t.Fatalf("expected complete-selection error, got %d and %q", exitCode, stderr.String())
	}
}

func TestWorkspaceReportsKubectlFailure(t *testing.T) {
	deps := workspaceTestDependencies(func(string, []string, io.Writer, io.Writer) error {
		return errors.New("command failed")
	})
	var stderr bytes.Buffer

	exitCode := runWithDependencies(
		[]string{
			"workspace", "render",
			"--adapter", "minikube",
			"--profile", "core",
			"--provider", "none",
		},
		&bytes.Buffer{},
		&stderr,
		deps,
	)

	if exitCode != 1 || !strings.Contains(stderr.String(), "kubectl render failed") {
		t.Fatalf("expected kubectl failure, got %d and %q", exitCode, stderr.String())
	}
}

func workspaceTestDependencies(runner commandRunner) dependencies {
	if runner == nil {
		runner = func(string, []string, io.Writer, io.Writer) error {
			return fmt.Errorf("unexpected command")
		}
	}
	return dependencies{
		lookPath:   alwaysAvailable,
		runCommand: runner,
		materialize: func(adapter string, _ deployment.Selection) (string, func(), error) {
			if adapter != "minikube" {
				return "", func() {}, fmt.Errorf("unsupported adapter %q", adapter)
			}
			return "/manifests/minikube", func() {}, nil
		},
		addRepo: func(repository.Request, io.Writer, io.Writer) error {
			return fmt.Errorf("unexpected repository materialization")
		},
	}
}

func alwaysAvailable(name string) (string, error) {
	return "/bin/" + name, nil
}
