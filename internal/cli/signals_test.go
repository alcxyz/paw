package cli

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCommandTerminationIsBounded(t *testing.T) {
	for _, test := range []struct {
		name     string
		repeated bool
		script   string
	}{
		{"grace deadline", false, "trap '' INT TERM; echo ready; while :; do :; done"},
		{"repeated signal", true, "trap '' INT TERM; echo ready; while :; do :; done"},
		{"stubborn descendant", false, `trap 'exit 0' TERM; sh -c 'trap "" INT TERM; echo ready; while :; do :; done' & wait`},
		{"interrupted zero exit", false, "trap 'exit 0' TERM; echo ready; while :; do :; done"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			signals := make(chan os.Signal, 2)
			ready := make(chan struct{})
			go func() {
				scanner := bufio.NewScanner(reader)
				if scanner.Scan() && scanner.Text() == "ready" {
					close(ready)
				}
			}()
			grace := 50 * time.Millisecond
			if test.repeated {
				grace = 10 * time.Second
			}
			done := make(chan error, 1)
			go func() {
				done <- executeCommandWithGrace("sh", []string{"-c", test.script}, writer, io.Discard, signals, grace)
			}()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				signals <- syscall.SIGTERM
				signals <- syscall.SIGTERM
				t.Fatal("child did not become ready")
			}
			signals <- syscall.SIGTERM
			if test.repeated {
				signals <- os.Interrupt
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted command reported success")
				}
				if test.name == "grace deadline" || test.repeated {
					var exitError *exec.ExitError
					if !errors.As(err, &exitError) {
						t.Fatalf("expected forced child termination, got %v", err)
					}
				}
			case <-time.After(2 * time.Second):
				signals <- syscall.SIGTERM
				t.Fatal("ignored termination was not bounded")
			}
		})
	}
}

func TestCommandHandlesClosedSignalChannel(t *testing.T) {
	signals := make(chan os.Signal)
	close(signals)
	if err := executeCommandWithSignals("sh", []string{"-c", "exit 0"}, io.Discard, io.Discard, signals); err != nil {
		t.Fatal(err)
	}
}
