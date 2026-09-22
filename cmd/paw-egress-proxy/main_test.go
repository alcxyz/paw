package main

import (
	"bytes"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunUnexpectedArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"extra-positional-arg"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unexpected arguments") {
		t.Errorf("stderr = %q, want mention of unexpected arguments", stderr.String())
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--not-a-real-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunMissingDestinationsWarnsAndStartsListening(t *testing.T) {
	var stdout, stderr bytes.Buffer

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"--listen", addr,
			"--destinations", "/nonexistent/path/does-not-exist",
		}, &stdout, &stderr)
	}()

	// Give the server a moment to start and fail to load the destination
	// file, then confirm it is actually listening before checking output.
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("server never started listening on %s: %v", addr, err)
	}
	conn.Close()

	// Signal a graceful shutdown, as the process's own signal handler
	// would, and confirm run() returns cleanly. Only inspect the shared
	// buffers after done receives, so there is a happens-before edge
	// (channel receive) between run()'s writes and this goroutine's reads.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal self: %v", err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("run() returned %d, want 0 after graceful shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after SIGTERM")
	}

	if !strings.Contains(stderr.String(), "destination list unavailable; denying all egress") {
		t.Errorf("stderr = %q, want warning about unavailable destination list", stderr.String())
	}
}
