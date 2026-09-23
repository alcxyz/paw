package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func defaultsTestDependencies(t *testing.T, env map[string]string, recorded *[][]string) dependencies {
	t.Helper()
	deps := workspaceTestDependencies(func(name string, args []string, stdout, stderr io.Writer) error {
		*recorded = append(*recorded, append([]string{name}, args...))
		return nil
	})
	configDir := t.TempDir()
	deps.configDir = func() (string, error) { return configDir, nil }
	deps.getenv = func(key string) string { return env[key] }
	return deps
}

func TestConfigSetShowUnsetRoundTrip(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, nil, &recorded)
	var stdout, stderr bytes.Buffer
	if code := runWithDependencies([]string{"config", "set", "adapter", "minikube"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("set adapter failed: %d %s", code, stderr.String())
	}
	if code := runWithDependencies([]string{"config", "set", "context", "paw-smoke"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("set context failed: %d %s", code, stderr.String())
	}
	if code := runWithDependencies([]string{"config", "set", "adapter", "docker"}, &stdout, &stderr, deps); code != 2 {
		t.Fatalf("unsupported adapter must be a usage error, got %d", code)
	}
	stdout.Reset()
	if code := runWithDependencies([]string{"config", "show"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("show failed: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ADAPTER    minikube") || !strings.Contains(stdout.String(), "CONTEXT    paw-smoke") {
		t.Fatalf("unexpected show output: %q", stdout.String())
	}
	path, err := userConfigPath(deps)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file should be private: %v %v", info, err)
	}
	if code := runWithDependencies([]string{"config", "unset", "context"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("unset failed: %d", code)
	}
	stdout.Reset()
	_ = runWithDependencies([]string{"config", "show"}, &stdout, &stderr, deps)
	if !strings.Contains(stdout.String(), "CONTEXT    (unset)") {
		t.Fatalf("context should be unset: %q", stdout.String())
	}
}

func TestDefaultsFillOmittedAdapterAndContext(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, nil, &recorded)
	_ = runWithDependencies([]string{"config", "set", "adapter", "minikube"}, io.Discard, io.Discard, deps)
	_ = runWithDependencies([]string{"config", "set", "context", "paw-smoke"}, io.Discard, io.Discard, deps)
	recorded = nil
	var stderr bytes.Buffer
	if code := runWithDependencies([]string{"ws", "inspect"}, io.Discard, &stderr, deps); code != 0 {
		t.Fatalf("inspect with defaults failed: %d %s", code, stderr.String())
	}
	if len(recorded) != 1 || !slices.Contains(recorded[0], "paw-smoke") {
		t.Fatalf("defaults were not applied: %v", recorded)
	}
}

func TestEnvironmentDefaultsWinOverFileAndFlagsWinOverBoth(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, map[string]string{"PAW_CONTEXT": "from-env"}, &recorded)
	_ = runWithDependencies([]string{"config", "set", "adapter", "minikube"}, io.Discard, io.Discard, deps)
	_ = runWithDependencies([]string{"config", "set", "context", "from-file"}, io.Discard, io.Discard, deps)
	recorded = nil
	_ = runWithDependencies([]string{"workspace", "inspect"}, io.Discard, io.Discard, deps)
	if len(recorded) != 1 || !slices.Contains(recorded[0], "from-env") {
		t.Fatalf("environment should win over the file: %v", recorded)
	}
	recorded = nil
	_ = runWithDependencies([]string{"workspace", "inspect", "--context", "from-flag"}, io.Discard, io.Discard, deps)
	if len(recorded) != 1 || !slices.Contains(recorded[0], "from-flag") {
		t.Fatalf("flag should win over the environment: %v", recorded)
	}
}

func TestRenderIgnoresDefaultContext(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, map[string]string{"PAW_ADAPTER": "minikube", "PAW_CONTEXT": "paw-smoke"}, &recorded)
	var stderr bytes.Buffer
	if code := runWithDependencies([]string{"workspace", "render", "--profile", "core", "--provider", "none"}, io.Discard, &stderr, deps); code != 0 {
		t.Fatalf("render should accept a default adapter and ignore the default context: %d %s", code, stderr.String())
	}
}

func TestInvalidConfigFileIsAnError(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, nil, &recorded)
	path, _ := userConfigPath(deps)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"adapter":"minikube","cluster":"typo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runWithDependencies([]string{"workspace", "inspect", "--context", "x"}, io.Discard, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "only \"adapter\" and \"context\"") {
		t.Fatalf("invalid config must fail loudly: %d %q", code, stderr.String())
	}
}

func TestRepoAliasAndLinkUseDefaults(t *testing.T) {
	var recorded [][]string
	deps := defaultsTestDependencies(t, map[string]string{"PAW_ADAPTER": "minikube", "PAW_CONTEXT": "paw-smoke"}, &recorded)
	deps.runCommand = func(name string, args []string, stdout, stderr io.Writer) error {
		recorded = append(recorded, append([]string{name}, args...))
		if name == "git" && args[0] == "config" {
			_, _ = io.WriteString(stdout, "user\n")
		}
		return nil
	}
	if code := runWithDependencies([]string{"ws", "repo", "link", "--name", "paw"}, io.Discard, io.Discard, deps); code != 0 {
		t.Fatalf("link with defaults failed: %d", code)
	}
	last := recorded[len(recorded)-1]
	if !strings.Contains(last[len(last)-1], "--adapter minikube --context paw-smoke --name paw") {
		t.Fatalf("link did not embed the defaults into the remote URL: %v", last)
	}
}

func TestInvocationLogRecordsCommandsNotOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "paw.log")
	getenv := func(key string) string {
		if key == "PAW_LOG_FILE" {
			return logPath
		}
		return ""
	}
	log := openInvocationLog(getenv)
	if log == nil {
		t.Fatal("log should open")
	}
	deps := log.withLogging(workspaceTestDependencies(func(name string, args []string, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "SECRET-OUTPUT")
		return nil
	}))
	code := runWithDependencies([]string{"workspace", "inspect", "--adapter", "minikube", "--context", "paw-smoke"}, io.Discard, io.Discard, deps)
	log.close([]string{"workspace", "inspect"}, code)
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, `"event":"command"`) || !strings.Contains(text, `"event":"invocation"`) || !strings.Contains(text, "paw-smoke") {
		t.Fatalf("log lacks expected events: %s", text)
	}
	if strings.Contains(text, "SECRET-OUTPUT") {
		t.Fatalf("log must not record command output: %s", text)
	}
	if openInvocationLog(func(key string) string {
		if key == "PAW_LOG" {
			return "0"
		}
		return ""
	}) != nil {
		t.Fatal("PAW_LOG=0 must disable logging")
	}
	var stdout bytes.Buffer
	deps.getenv = getenv
	if code := runWithDependencies([]string{"logs", "path"}, &stdout, io.Discard, deps); code != 0 || strings.TrimSpace(stdout.String()) != logPath {
		t.Fatalf("logs path: %d %q", code, stdout.String())
	}
	stdout.Reset()
	if code := runWithDependencies([]string{"logs", "tail", "1"}, &stdout, io.Discard, deps); code != 0 || !strings.Contains(stdout.String(), `"event":"invocation"`) {
		t.Fatalf("logs tail: %d %q", code, stdout.String())
	}
}

func TestXDGConfigDirHonoursTheVariableOnEveryPlatform(t *testing.T) {
	dir, err := xdgConfigDir(func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return "/tmp/xdg-test"
		}
		return ""
	})
	if err != nil || dir != "/tmp/xdg-test" {
		t.Fatalf("XDG_CONFIG_HOME should win: %q %v", dir, err)
	}
	dir, err = xdgConfigDir(func(string) string { return "" })
	if err != nil || filepath.Base(dir) != ".config" {
		t.Fatalf("default should be ~/.config on every platform: %q %v", dir, err)
	}
	if _, err := xdgConfigDir(func(string) string { return "relative/path" }); err == nil {
		t.Fatal("a relative XDG_CONFIG_HOME must be rejected")
	}
}
