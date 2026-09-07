package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"git.alc.xyz/alcxyz/paw/internal/environment"
)

func TestCreateDoesNotApplyAfterFailedNamespaceReservation(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		calls := 0
		deps := workspaceTestDependencies(func(_ string, _ []string, _, _ io.Writer) error {
			calls++
			if calls == failAt {
				return errors.New("rejected")
			}
			return nil
		})
		var stderr bytes.Buffer
		exit := runWorkspaceCreate(workspaceOptions{context: "test"}, "/manifests/minikube", io.Discard, &stderr, deps)
		if exit != 1 || calls != failAt {
			t.Fatalf("failure at step %d: exit=%d calls=%d", failAt, exit, calls)
		}
		if failAt > 1 && !strings.Contains(stderr.String(), "retained for inspection") {
			t.Fatalf("missing partial-creation diagnostic: %s", stderr.String())
		}
	}
}

func TestCreateRejectsUnprovenEnvironmentBeforeNamespaceReservation(t *testing.T) {
	for _, outcome := range []environment.Outcome{environment.OutcomeFail, environment.OutcomeInconclusive, environment.OutcomeError} {
		called := false
		deps := workspaceTestDependencies(func(_ string, _ []string, _, _ io.Writer) error { called = true; return nil })
		deps.verifyEnv = func(_ context.Context, request environment.Request) (environment.Report, error) {
			if request.Context != "test" {
				t.Fatalf("wrong context: %s", request.Context)
			}
			return environment.Report{Outcome: outcome}, nil
		}
		if exit := runWorkspaceCreate(workspaceOptions{context: "test"}, "/manifests/minikube", io.Discard, io.Discard, deps); exit != 1 || called {
			t.Fatalf("creation proceeded for %s", outcome)
		}
	}
}

const ownedNamespaceJSON = `{"metadata":{"name":"paw-workspace","uid":"original-uid","resourceVersion":"42","labels":{"paw.alc.xyz/managed-by":"paw"}}}`

func TestDestroyBindsDeletionToInspectedNamespace(t *testing.T) {
	var commands [][]string
	var requestFile string
	deps := workspaceTestDependencies(func(_ string, args []string, stdout, _ io.Writer) error {
		commands = append(commands, slices.Clone(args))
		if slices.Contains(args, "get") {
			_, _ = io.WriteString(stdout, ownedNamespaceJSON)
		}
		if slices.Contains(args, "delete") {
			if !slices.Contains(args, "--raw=/api/v1/namespaces/paw-workspace") {
				t.Fatalf("unscoped delete: %v", args)
			}
			requestFile = args[len(args)-1]
			data, err := os.ReadFile(requestFile)
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Preconditions map[string]string `json:"preconditions"`
			}
			if err := json.Unmarshal(data, &request); err != nil {
				t.Fatal(err)
			}
			if request.Preconditions["uid"] != "original-uid" || request.Preconditions["resourceVersion"] != "42" {
				t.Fatalf("missing deletion preconditions: %s", data)
			}
		}
		return nil
	})
	if exit := runWorkspaceDestroy(workspaceOptions{context: "test"}, io.Discard, io.Discard, deps); exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(commands) != 3 || !slices.Contains(commands[2], "--for=delete") {
		t.Fatalf("missing bounded deletion wait: %v", commands)
	}
	for _, command := range commands {
		if command[0] != "--context" || command[1] != "test" {
			t.Fatalf("wrong context: %v", command)
		}
	}
	if _, err := os.Stat(requestFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deletion request was retained: %v", err)
	}
}

func TestDestroyRefusesUnownedOrUnverifiedNamespaces(t *testing.T) {
	for _, response := range []string{
		`{`, `{}`, `{"metadata":{"name":"paw-workspace","uid":"x","resourceVersion":"1","labels":{"app.kubernetes.io/name":"paw"}}}`,
		strings.Replace(ownedNamespaceJSON, "paw-workspace", "other", 1),
		strings.Replace(ownedNamespaceJSON, "original-uid", "", 1),
	} {
		calls := 0
		deps := workspaceTestDependencies(func(_ string, _ []string, stdout, _ io.Writer) error {
			calls++
			_, _ = io.WriteString(stdout, response)
			return nil
		})
		if exit := runWorkspaceDestroy(workspaceOptions{context: "test"}, io.Discard, io.Discard, deps); exit != 1 || calls != 1 {
			t.Fatalf("unsafe deletion for %q: exit=%d calls=%d", response, exit, calls)
		}
	}
}

func TestDestroyFailureAndAbsence(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3} {
		calls := 0
		deps := workspaceTestDependencies(func(_ string, _ []string, stdout, _ io.Writer) error {
			calls++
			if calls == failAt {
				return errors.New("API rejected operation")
			}
			if calls == 1 && failAt != 0 {
				_, _ = io.WriteString(stdout, ownedNamespaceJSON)
			}
			return nil
		})
		exit := runWorkspaceDestroy(workspaceOptions{context: "test"}, io.Discard, io.Discard, deps)
		if failAt == 0 {
			if exit != 0 || calls != 1 {
				t.Fatalf("absent namespace: exit=%d calls=%d", exit, calls)
			}
		} else if exit != 1 || calls != failAt {
			t.Fatalf("failure %d: exit=%d calls=%d", failAt, exit, calls)
		}
	}
}
