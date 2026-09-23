package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// invocationLog is an append-only JSON-lines record of what PAW did: each
// invocation, every subprocess it ran, exit codes, and durations. It never
// records command output, so pairing credentials, provider prompts, and Git
// streams stay out of it. It lives under the XDG state directory and is meant
// to be attached to bug reports. PAW_LOG=0 disables it; PAW_LOG_FILE overrides
// the location.
type invocationLog struct {
	file    *os.File
	started time.Time
}

const (
	logDisableVariable = "PAW_LOG"
	logFileVariable    = "PAW_LOG_FILE"
	logFileName        = "paw.log"
)

func userLogPath(getenv func(string) string) (string, error) {
	if explicit := strings.TrimSpace(getenv(logFileVariable)); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", fmt.Errorf("%s must be an absolute path", logFileVariable)
		}
		return explicit, nil
	}
	state := strings.TrimSpace(getenv("XDG_STATE_HOME"))
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("no home directory is available for the state log")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "paw", logFileName), nil
}

// openInvocationLog opens the log for appending. Any failure yields a nil
// log, which records nothing: logging must never block the command.
func openInvocationLog(getenv func(string) string) *invocationLog {
	if value := strings.TrimSpace(getenv(logDisableVariable)); value == "0" || strings.EqualFold(value, "off") || strings.EqualFold(value, "false") {
		return nil
	}
	path, err := userLogPath(getenv)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return &invocationLog{file: file, started: time.Now()}
}

func (l *invocationLog) event(kind string, fields map[string]any) {
	if l == nil {
		return
	}
	record := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "event": kind}
	for key, value := range fields {
		record[key] = value
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = l.file.Write(append(line, '\n'))
}

func (l *invocationLog) close(args []string, exitCode int) {
	if l == nil {
		return
	}
	l.event("invocation", map[string]any{
		"args": args, "exit": exitCode, "ms": time.Since(l.started).Milliseconds(),
	})
	_ = l.file.Close()
}

// withLogging wraps the subprocess runners so every command PAW executes is
// recorded with its arguments, duration, and outcome, but never its output.
func (l *invocationLog) withLogging(deps dependencies) dependencies {
	if l == nil {
		return deps
	}
	runCommand := deps.runCommand
	deps.runCommand = func(name string, args []string, stdout, stderr io.Writer) error {
		started := time.Now()
		err := runCommand(name, args, stdout, stderr)
		l.commandEvent(name, args, started, err)
		return err
	}
	runInput := deps.runInput
	deps.runInput = func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		started := time.Now()
		err := runInput(ctx, name, args, stdin, stdout, stderr)
		l.commandEvent(name, args, started, err)
		return err
	}
	runInteractive := deps.runInteractive
	deps.runInteractive = func(name string, args []string) error {
		started := time.Now()
		err := runInteractive(name, args)
		l.commandEvent(name, args, started, err)
		return err
	}
	return deps
}

func (l *invocationLog) commandEvent(name string, args []string, started time.Time, err error) {
	fields := map[string]any{"name": name, "args": args, "ms": time.Since(started).Milliseconds(), "ok": err == nil}
	if err != nil {
		fields["error"] = err.Error()
	}
	l.event("command", fields)
}

func logsUsage() string {
	return `usage:
  paw logs path
  paw logs tail [N]

The invocation log records what PAW ran and how it ended, never command
output. Attach it to bug reports. PAW_LOG=0 disables it; PAW_LOG_FILE moves
it; the default is $XDG_STATE_HOME/paw/paw.log.`
}

func runLogs(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, logsUsage())
	}
	path, err := userLogPath(environmentLookup(deps))
	if err != nil {
		fmt.Fprintf(stderr, "paw: %v\n", err)
		return 1
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return usageError(stderr, logsUsage())
		}
		fmt.Fprintln(stdout, path)
		return 0
	case "tail":
		count := 50
		if len(args) == 2 {
			parsed, err := strconv.Atoi(args[1])
			if err != nil || parsed <= 0 {
				return usageError(stderr, "tail count must be a positive integer")
			}
			count = parsed
		} else if len(args) != 1 {
			return usageError(stderr, logsUsage())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "paw: no invocation log at %s\n", path)
			return 1
		}
		lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
		if len(lines) > count {
			lines = lines[len(lines)-count:]
		}
		for _, line := range lines {
			fmt.Fprintln(stdout, line)
		}
		return 0
	default:
		return usageError(stderr, logsUsage())
	}
}
