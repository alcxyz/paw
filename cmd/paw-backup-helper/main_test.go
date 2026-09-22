package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelperRejectsUnknownCommandsAndOptions(t *testing.T) {
	for _, args := range [][]string{nil, {"exec", "sh"}, {"import", "unexpected"}, {"export", "--secret-input", "do-not-print"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Fatalf("expected usage refusal, got %d", code)
		}
		if stdout.Len() != 0 || strings.Contains(stderr.String(), "do-not-print") {
			t.Fatal("helper exposed untrusted input")
		}
	}
}

func TestHelperServeStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"serve"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("serve returned %d", code)
	}
}
