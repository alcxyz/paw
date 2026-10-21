package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	deployment "github.com/alcxyz/paw/deploy"
	"github.com/alcxyz/paw/internal/environment"
)

func TestUnavailableProviderCreateAndRenderHaveNoSideEffects(t *testing.T) {
	for _, adapter := range []string{"minikube", "kubernetes"} {
		for _, profile := range []string{"core", "developer", "platform-readonly"} {
			for _, provider := range []string{"codex", "claude-code", "opencode", "all"} {
				for _, operation := range []string{"create", "render"} {
					args := []string{"workspace", operation, "--adapter", adapter, "--profile", profile, "--provider", provider}
					if operation == "create" {
						args = append(args, "--context", "fixture")
					}
					deps := workspaceTestDependencies(func(string, []string, io.Writer, io.Writer) error { t.Fatal("Kubernetes must not run"); return nil })
					deps.materialize = func(deployment.ManifestRequest) (string, func(), error) {
						t.Fatal("materializer must not run")
						return "", func() {}, nil
					}
					deps.verifyEnv = func(context.Context, environment.Request) (environment.Report, error) {
						t.Fatal("verification must not run")
						return environment.Report{}, nil
					}
					var stdout, stderr bytes.Buffer
					if exit := runWithDependencies(args, &stdout, &stderr, deps); exit != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "application-destination enforcement") {
						t.Fatalf("%v: exit=%d, stdout=%q, stderr=%q", args, exit, &stdout, &stderr)
					}
				}
			}
		}
	}
}
