package environment

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type runnerCall struct {
	args  []string
	stdin string
}

type probeRunner struct {
	calls              []runnerCall
	namespaceCollision bool
	policiesCreated    bool
	positiveFailure    bool
	negativeError      bool
	networkEnforced    bool
	cleanupFailure     bool
	namespaceCreated   bool
	namespaceCreateErr bool
	readinessTimeout   bool
	readinessCanceled  bool
}

func (r *probeRunner) run(
	_ context.Context,
	name string,
	stdin io.Reader,
	args ...string,
) (commandResult, error) {
	if name != "kubectl" {
		return commandResult{}, errors.New("unexpected command")
	}
	input := ""
	if stdin != nil {
		value, err := io.ReadAll(stdin)
		if err != nil {
			return commandResult{}, err
		}
		input = string(value)
	}
	r.calls = append(r.calls, runnerCall{args: slices.Clone(args), stdin: input})
	joined := strings.Join(args, " ")

	switch {
	case strings.Contains(joined, "version --output=json"):
		return commandResult{stdout: `{"serverVersion":{"gitVersion":"v1.33.7"}}`}, nil
	case strings.Contains(joined, "get namespace") && strings.Contains(joined, "verification-id"):
		if r.namespaceCreated {
			return commandResult{stdout: "paw-verify-fixed"}, nil
		}
		return commandResult{}, nil
	case strings.Contains(joined, "get namespace"):
		if r.namespaceCollision {
			return commandResult{stdout: "namespace/paw-verify-fixed\n"}, nil
		}
		return commandResult{}, nil
	case strings.Contains(input, "kind: Namespace"):
		r.namespaceCreated = true
		if r.namespaceCreateErr {
			return commandResult{}, errors.New("request result was lost")
		}
		return commandResult{}, nil
	case strings.Contains(input, "kind: Pod"):
		return commandResult{}, nil
	case strings.Contains(joined, "wait --for=condition=Ready"):
		if r.readinessTimeout {
			return commandResult{}, context.DeadlineExceeded
		}
		if r.readinessCanceled {
			return commandResult{}, context.Canceled
		}
		return commandResult{}, nil
	case strings.Contains(joined, "get pod egress-target"):
		return commandResult{stdout: "10.0.0.10"}, nil
	case strings.Contains(joined, "get pod ingress-target"):
		return commandResult{stdout: "10.0.0.11"}, nil
	case strings.Contains(input, "kind: NetworkPolicy"):
		r.policiesCreated = true
		return commandResult{}, nil
	case strings.Contains(joined, "exec pod/"):
		if !r.policiesCreated {
			if r.positiveFailure {
				return commandResult{stderr: "unable to upgrade connection"}, errors.New("exit status 1")
			}
			return commandResult{}, nil
		}
		if r.negativeError {
			return commandResult{stderr: "pod disappeared"}, errors.New("exit status 1")
		}
		if r.networkEnforced {
			return commandResult{stderr: "TIMEOUT\ncommand terminated with exit code 1"}, errors.New("exit status 1")
		}
		return commandResult{}, nil
	case strings.Contains(joined, "delete namespace"):
		if r.cleanupFailure {
			return commandResult{}, errors.New("exit status 1")
		}
		return commandResult{}, nil
	default:
		return commandResult{}, errors.New("unexpected kubectl invocation")
	}
}

func testVerifier(runner runner) verifier {
	now := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	return verifier{
		runner: runner,
		now: func() time.Time {
			now = now.Add(10 * time.Second)
			return now
		},
		newNamespace: func() (string, error) { return "paw-verify-fixed", nil },
		sleep:        func(context.Context, time.Duration) error { return nil },
	}
}

func TestVerifyPassesIndependentIngressAndEgressControls(t *testing.T) {
	runner := &probeRunner{networkEnforced: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err != nil {
		t.Fatalf("verify returned an error: %v", err)
	}
	if report.Outcome != OutcomePass {
		t.Fatalf("expected pass, got %s: %#v", report.Outcome, report.Checks)
	}
	for _, name := range []string{
		"egress-positive-control",
		"ingress-positive-control",
		"default-deny-egress",
		"default-deny-ingress",
		"cleanup",
	} {
		if !hasCheck(report, name, StatusPass) {
			t.Fatalf("missing passing %s check: %#v", name, report.Checks)
		}
	}
	if report.Context != "test-context" || report.KubernetesVersion != "v1.33.7" {
		t.Fatalf("unexpected report metadata: %#v", report)
	}
	if !runner.deletedNamespace() {
		t.Fatal("probe namespace was not deleted")
	}
}

func TestVerifyFailsWhenPoliciesDoNotEnforce(t *testing.T) {
	runner := &probeRunner{}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err != nil {
		t.Fatalf("non-enforcement should be a report outcome, got error: %v", err)
	}
	if report.Outcome != OutcomeFail ||
		!hasCheck(report, "default-deny-egress", StatusFail) ||
		!hasCheck(report, "default-deny-ingress", StatusFail) {
		t.Fatalf("expected ingress and egress enforcement failures: %#v", report)
	}
	if !runner.deletedNamespace() {
		t.Fatal("probe namespace was not deleted")
	}
}

func TestVerifyTreatsFailedPositiveControlAsInconclusive(t *testing.T) {
	runner := &probeRunner{positiveFailure: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err != nil {
		t.Fatalf("positive-control failure should be reported, got error: %v", err)
	}
	if report.Outcome != OutcomeInconclusive ||
		!hasCheck(report, "egress-positive-control", StatusInconclusive) {
		t.Fatalf("expected inconclusive positive control: %#v", report)
	}
	if runner.policiesCreated {
		t.Fatal("policies were created after the positive control failed")
	}
	if !runner.deletedNamespace() {
		t.Fatal("probe namespace was not deleted")
	}
}

func TestVerifyTreatsReadinessTimeoutAsInconclusiveAndCleansUp(t *testing.T) {
	runner := &probeRunner{readinessTimeout: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err != nil {
		t.Fatalf("readiness timeout should be reported, got error: %v", err)
	}
	if report.Outcome != OutcomeInconclusive ||
		!hasCheck(report, "probe-readiness", StatusInconclusive) {
		t.Fatalf("expected inconclusive readiness timeout: %#v", report)
	}
	if !runner.deletedNamespace() || !hasCheck(report, "cleanup", StatusPass) {
		t.Fatalf("timed-out probe namespace was not cleaned: %#v", report.Checks)
	}
}

func TestVerifyUsesIndependentCleanupAfterInterruption(t *testing.T) {
	runner := &probeRunner{readinessCanceled: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err != nil {
		t.Fatalf("interrupted readiness should be reported, got error: %v", err)
	}
	if report.Outcome != OutcomeInconclusive || !runner.deletedNamespace() ||
		!hasCheck(report, "cleanup", StatusPass) {
		t.Fatalf("interrupted run did not clean up independently: %#v", report)
	}
}

func TestVerifyRefusesNamespaceCollisionWithoutDeletingIt(t *testing.T) {
	runner := &probeRunner{namespaceCollision: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err == nil || report.Outcome != OutcomeError {
		t.Fatalf("expected collision error, got report=%#v error=%v", report, err)
	}
	if !hasCheck(report, "namespace-collision", StatusError) {
		t.Fatalf("collision was not reported: %#v", report.Checks)
	}
	if runner.deletedNamespace() {
		t.Fatal("verifier attempted to delete a namespace it did not create")
	}
}

func TestVerifyCleansNamespaceAfterAmbiguousCreateFailure(t *testing.T) {
	runner := &probeRunner{namespaceCreateErr: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err == nil || report.Outcome != OutcomeError {
		t.Fatalf("expected create error, got report=%#v error=%v", report, err)
	}
	if !runner.deletedNamespace() || !hasCheck(report, "cleanup", StatusPass) {
		t.Fatalf("owned namespace was not cleaned after ambiguous create: %#v", report.Checks)
	}
}

func TestVerifyDoesNotTreatKubectlFailureAsNetworkDenial(t *testing.T) {
	runner := &probeRunner{negativeError: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err == nil || report.Outcome != OutcomeError {
		t.Fatalf("expected operational error, got report=%#v error=%v", report, err)
	}
	if hasCheck(report, "default-deny-egress", StatusPass) {
		t.Fatal("kubectl failure was incorrectly treated as policy enforcement")
	}
	if !runner.deletedNamespace() {
		t.Fatal("probe namespace was not deleted")
	}
}

func TestVerifyMakesCleanupFailureFatal(t *testing.T) {
	runner := &probeRunner{networkEnforced: true, cleanupFailure: true}
	report, err := testVerifier(runner).verify(
		context.Background(),
		Request{Context: "test-context"},
	)
	if err == nil || report.Outcome != OutcomeError || !hasCheck(report, "cleanup", StatusError) {
		t.Fatalf("expected fatal cleanup result, got report=%#v error=%v", report, err)
	}
}

func TestProbeManifestsUseRestrictedPinnedWorkloads(t *testing.T) {
	manifest := podManifest("paw-verify-fixed")
	if strings.Count(manifest, "kind: Pod") != 4 {
		t.Fatalf("expected four probe pods: %s", manifest)
	}
	for _, required := range []string{
		probeImage,
		"automountServiceAccountToken: false",
		"runAsNonRoot: true",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		`drop: ["ALL"]`,
	} {
		if !strings.Contains(manifest, required) {
			t.Fatalf("probe manifest lacks %q", required)
		}
	}
	if strings.Contains(manifest, "hostPath:") || strings.Contains(manifest, "privileged: true") {
		t.Fatalf("probe manifest contains forbidden host access: %s", manifest)
	}
	if strings.Contains(manifest, "netexec") || !strings.Contains(manifest, "serve-hostname") {
		t.Fatalf("probe targets must expose only the hostname TCP server: %s", manifest)
	}
}

func TestProbeManifestsPassKubectlClientValidation(t *testing.T) {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("kubectl is not available")
	}
	manifest := strings.Join([]string{
		namespaceManifest("paw-verify-fixed"),
		podManifest("paw-verify-fixed"),
		policyManifest("paw-verify-fixed"),
	}, "---\n")
	command := exec.Command(kubectl,
		"create", "--dry-run=client", "--validate=false", "--filename=-",
	)
	command.Stdin = strings.NewReader(manifest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("kubectl rejected probe manifests: %v\n%s", err, output)
	}
}

func hasCheck(report Report, name string, status Status) bool {
	for _, check := range report.Checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func (r *probeRunner) deletedNamespace() bool {
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call.args, " "), "delete namespace paw-verify-fixed") {
			return true
		}
	}
	return false
}
