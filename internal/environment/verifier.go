// Package environment verifies that a selected runtime environment enforces
// the behavioral contracts required by PAW.
package environment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	ContractVersion = "v0"
	ProbeVersion    = "network-policy-v1"
	SchemaVersion   = "paw.environment.verify/v1"

	probeImage = "registry.k8s.io/e2e-test-images/agnhost@sha256:" +
		"99c6b4bb4a1e1df3f0b3752168c89358794d02258ebebc26bf21c29399011a85"

	apiTimeout       = 10 * time.Second
	cleanupTimeout   = 30 * time.Second
	podReadyTimeout  = 2 * time.Minute
	probeTimeout     = 3 * time.Second
	policyDeadline   = 20 * time.Second
	policyRetryDelay = time.Second
)

// Outcome is the overall result of an environment verification.
type Outcome string

const (
	OutcomePass         Outcome = "pass"
	OutcomeFail         Outcome = "fail"
	OutcomeInconclusive Outcome = "inconclusive"
	OutcomeError        Outcome = "error"
)

// Status is the result of one verification check.
type Status string

const (
	StatusPass         Status = "pass"
	StatusFail         Status = "fail"
	StatusInconclusive Status = "inconclusive"
	StatusError        Status = "error"
)

// Request selects the Kubernetes context to verify.
type Request struct {
	Context string
}

// Check records one credential-free behavioral outcome.
type Check struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
}

// Report is the minimal evidence produced by an environment verification.
type Report struct {
	SchemaVersion     string    `json:"schemaVersion"`
	ContractVersion   string    `json:"contractVersion"`
	ProbeVersion      string    `json:"probeVersion"`
	Context           string    `json:"context"`
	KubernetesVersion string    `json:"kubernetesVersion,omitempty"`
	StartedAt         time.Time `json:"startedAt"`
	CompletedAt       time.Time `json:"completedAt"`
	Outcome           Outcome   `json:"outcome"`
	Checks            []Check   `json:"checks"`
}

// Verify runs the network-policy conformance probe against one explicit
// Kubernetes context. It creates and deletes only a uniquely named namespace.
func Verify(ctx context.Context, request Request) (Report, error) {
	verifier := verifier{
		runner:       execRunner{},
		now:          func() time.Time { return time.Now().UTC() },
		newNamespace: randomNamespace,
		sleep:        sleepContext,
	}
	return verifier.verify(ctx, request)
}

// WriteJSON writes a stable machine-readable report.
func WriteJSON(output io.Writer, report Report) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// WriteText writes a concise human-readable report.
func WriteText(output io.Writer, report Report) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "OUTCOME\t%s\n", report.Outcome); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "CONTEXT\t%s\n", report.Context); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "KUBERNETES\t%s\n", report.KubernetesVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "CONTRACT\t%s\n", report.ContractVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "PROBE\t%s\n", report.ProbeVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "STARTED\t%s\n", report.StartedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "COMPLETED\t%s\n", report.CompletedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "\nCHECK\tSTATUS\tDETAIL"); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", check.Name, check.Status, check.Message); err != nil {
			return err
		}
	}
	return w.Flush()
}

type commandResult struct {
	stdout string
	stderr string
}

type runner interface {
	run(context.Context, string, io.Reader, ...string) (commandResult, error)
}

type execRunner struct{}

func (execRunner) run(
	ctx context.Context,
	name string,
	stdin io.Reader,
	args ...string,
) (commandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String()}, err
}

type verifier struct {
	runner       runner
	now          func() time.Time
	newNamespace func() (string, error)
	sleep        func(context.Context, time.Duration) error
}

func (v verifier) verify(ctx context.Context, request Request) (Report, error) {
	started := v.now()
	report := Report{
		SchemaVersion:   SchemaVersion,
		ContractVersion: ContractVersion,
		ProbeVersion:    ProbeVersion,
		Context:         request.Context,
		StartedAt:       started,
		Outcome:         OutcomeError,
		Checks:          []Check{},
	}

	if request.Context == "" {
		report.CompletedAt = v.now()
		return report, errors.New("context must not be empty")
	}
	namespace, err := v.newNamespace()
	if err != nil {
		report.CompletedAt = v.now()
		return report, fmt.Errorf("generate verification namespace: %w", err)
	}

	created := false
	runErr := v.run(ctx, request.Context, namespace, &created, &report)
	cleanupErr := v.cleanup(request.Context, namespace, created, &report)
	report.CompletedAt = v.now()

	if cleanupErr != nil {
		report.Outcome = OutcomeError
		if runErr != nil {
			return report, fmt.Errorf("%v; cleanup: %w", runErr, cleanupErr)
		}
		return report, fmt.Errorf("cleanup: %w", cleanupErr)
	}
	if runErr != nil {
		return report, runErr
	}
	return report, nil
}

func (v verifier) run(
	ctx context.Context,
	contextName string,
	namespace string,
	created *bool,
	report *Report,
) error {
	version, err := v.serverVersion(ctx, contextName)
	if err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "api-access", Status: StatusError,
			Message: "could not read the selected Kubernetes server version",
		})
		return err
	}
	report.KubernetesVersion = version
	report.Checks = append(report.Checks, Check{
		Name: "api-access", Status: StatusPass,
		Message: "selected context answered a bounded version request",
	})

	exists, err := v.namespaceExists(ctx, contextName, namespace)
	if err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "namespace-collision", Status: StatusError,
			Message: "could not establish that the probe namespace is absent",
		})
		return err
	}
	if exists {
		report.Checks = append(report.Checks, Check{
			Name: "namespace-collision", Status: StatusError,
			Message: "generated namespace already exists; nothing was changed",
		})
		return errors.New("generated verification namespace already exists")
	}
	report.Checks = append(report.Checks, Check{
		Name: "namespace-collision", Status: StatusPass,
		Message: "generated namespace was absent before creation",
	})

	if err := v.createNamespace(ctx, contextName, namespace); err != nil {
		owned, ownershipErr := v.namespaceOwned(contextName, namespace)
		if ownershipErr == nil && owned {
			*created = true
		}
		report.Checks = append(report.Checks, Check{
			Name: "namespace-create", Status: StatusError,
			Message: "the isolated probe namespace could not be created",
		})
		if ownershipErr != nil {
			return fmt.Errorf("%v; establish namespace ownership after failure: %w", err, ownershipErr)
		}
		return err
	}
	*created = true
	report.Checks = append(report.Checks, Check{
		Name: "namespace-create", Status: StatusPass,
		Message: "created an isolated restricted namespace",
	})

	if err := v.createProbePods(ctx, contextName, namespace); err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "probe-readiness", Status: StatusError,
			Message: "probe workloads could not be created",
		})
		return err
	}
	if err := v.waitForProbePods(ctx, contextName, namespace); err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "probe-readiness", Status: StatusInconclusive,
			Message: "probe workloads did not become ready before the deadline",
		})
		report.Outcome = OutcomeInconclusive
		return nil
	}
	report.Checks = append(report.Checks, Check{
		Name: "probe-readiness", Status: StatusPass,
		Message: "all in-cluster probe workloads became ready",
	})

	egressTarget, err := v.podIP(ctx, contextName, namespace, "egress-target")
	if err != nil {
		return err
	}
	ingressTarget, err := v.podIP(ctx, contextName, namespace, "ingress-target")
	if err != nil {
		return err
	}

	egressReachable, _ := v.probe(ctx, contextName, namespace, "egress-client", egressTarget)
	if !egressReachable {
		report.Checks = append(report.Checks, Check{
			Name: "egress-positive-control", Status: StatusInconclusive,
			Message: "the future selected client could not reach its target before policy",
		})
		report.Outcome = OutcomeInconclusive
		return nil
	}
	report.Checks = append(report.Checks, Check{
		Name: "egress-positive-control", Status: StatusPass,
		Message: "the future selected client reached its target before policy",
	})

	ingressReachable, _ := v.probe(ctx, contextName, namespace, "ingress-client", ingressTarget)
	if !ingressReachable {
		report.Checks = append(report.Checks, Check{
			Name: "ingress-positive-control", Status: StatusInconclusive,
			Message: "the client could not reach the future selected target before policy",
		})
		report.Outcome = OutcomeInconclusive
		return nil
	}
	report.Checks = append(report.Checks, Check{
		Name: "ingress-positive-control", Status: StatusPass,
		Message: "the client reached the future selected target before policy",
	})

	if err := v.createPolicies(ctx, contextName, namespace); err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "network-policy-create", Status: StatusError,
			Message: "default-deny probe policies could not be created",
		})
		return err
	}
	report.Checks = append(report.Checks, Check{
		Name: "network-policy-create", Status: StatusPass,
		Message: "created standard ingress and egress NetworkPolicy probes",
	})

	egressDenied, err := v.waitForDenial(
		ctx, contextName, namespace, "egress-client", egressTarget,
	)
	if err != nil {
		return err
	}
	isolationFailed := false
	if !egressDenied {
		report.Checks = append(report.Checks, Check{
			Name: "default-deny-egress", Status: StatusFail,
			Message: "the selected client retained egress after the convergence deadline",
		})
		isolationFailed = true
	} else {
		report.Checks = append(report.Checks, Check{
			Name: "default-deny-egress", Status: StatusPass,
			Message: "the selected client could not reach the proven in-cluster target",
		})
	}

	ingressDenied, err := v.waitForDenial(
		ctx, contextName, namespace, "ingress-client", ingressTarget,
	)
	if err != nil {
		return err
	}
	if !ingressDenied {
		report.Checks = append(report.Checks, Check{
			Name: "default-deny-ingress", Status: StatusFail,
			Message: "the selected target remained reachable after the convergence deadline",
		})
		isolationFailed = true
	} else {
		report.Checks = append(report.Checks, Check{
			Name: "default-deny-ingress", Status: StatusPass,
			Message: "the selected target could not be reached over the proven in-cluster path",
		})
	}
	if isolationFailed {
		report.Outcome = OutcomeFail
		return nil
	}
	report.Outcome = OutcomePass
	return nil
}

func (v verifier) serverVersion(ctx context.Context, contextName string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	result, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName,
		"--request-timeout=8s",
		"version", "--output=json",
	)
	if err != nil {
		return "", fmt.Errorf("read Kubernetes server version: %w", err)
	}
	var value struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &value); err != nil || value.ServerVersion.GitVersion == "" {
		return "", errors.New("read Kubernetes server version: invalid response")
	}
	return value.ServerVersion.GitVersion, nil
}

func (v verifier) namespaceExists(
	ctx context.Context,
	contextName string,
	namespace string,
) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	result, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName,
		"--request-timeout=8s",
		"get", "namespace", namespace,
		"--ignore-not-found=true",
		"--output=name",
	)
	if err != nil {
		return false, fmt.Errorf("check verification namespace: %w", err)
	}
	return strings.TrimSpace(result.stdout) != "", nil
}

func (v verifier) createNamespace(ctx context.Context, contextName, namespace string) error {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	_, err := v.runner.run(commandCtx, "kubectl", strings.NewReader(namespaceManifest(namespace)),
		"--context", contextName,
		"--request-timeout=8s",
		"create", "--filename=-",
	)
	if err != nil {
		return fmt.Errorf("create verification namespace: %w", err)
	}
	return nil
}

func (v verifier) namespaceOwned(contextName, namespace string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	result, err := v.runner.run(ctx, "kubectl", nil,
		"--context", contextName,
		"--request-timeout=8s",
		"get", "namespace", namespace,
		`--output=jsonpath={.metadata.labels.paw\.alc\.xyz/verification-id}`,
	)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(result.stdout) == namespace, nil
}

func (v verifier) createProbePods(ctx context.Context, contextName, namespace string) error {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	_, err := v.runner.run(commandCtx, "kubectl", strings.NewReader(podManifest(namespace)),
		"--context", contextName,
		"--request-timeout=8s",
		"create", "--filename=-",
	)
	if err != nil {
		return fmt.Errorf("create verification probes: %w", err)
	}
	return nil
}

func (v verifier) waitForProbePods(ctx context.Context, contextName, namespace string) error {
	commandCtx, cancel := context.WithTimeout(ctx, podReadyTimeout+apiTimeout)
	defer cancel()
	_, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName,
		"--namespace", namespace,
		"--request-timeout=8s",
		"wait", "--for=condition=Ready", "pod", "--all",
		"--timeout="+podReadyTimeout.String(),
	)
	return err
}

func (v verifier) podIP(ctx context.Context, contextName, namespace, pod string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	result, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName,
		"--namespace", namespace,
		"--request-timeout=8s",
		"get", "pod", pod,
		"--output=jsonpath={.status.podIP}",
	)
	if err != nil {
		return "", fmt.Errorf("read probe address: %w", err)
	}
	address := strings.TrimSpace(result.stdout)
	if _, err := netip.ParseAddr(address); err != nil {
		return "", errors.New("read probe address: pod returned an invalid address")
	}
	return address, nil
}

func (v verifier) createPolicies(ctx context.Context, contextName, namespace string) error {
	commandCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	_, err := v.runner.run(commandCtx, "kubectl", strings.NewReader(policyManifest(namespace)),
		"--context", contextName,
		"--request-timeout=8s",
		"create", "--filename=-",
	)
	if err != nil {
		return fmt.Errorf("create verification policies: %w", err)
	}
	return nil
}

func (v verifier) probe(
	ctx context.Context,
	contextName string,
	namespace string,
	pod string,
	target string,
) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, probeTimeout+apiTimeout)
	defer cancel()
	result, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName,
		"--namespace", namespace,
		"--request-timeout=8s",
		"exec", "pod/"+pod, "--",
		"/agnhost", "connect",
		"--timeout="+probeTimeout.String(),
		"--protocol=tcp",
		netip.AddrPortFrom(netip.MustParseAddr(target), 8080).String(),
	)
	if err == nil {
		return true, nil
	}
	if commandCtx.Err() != nil {
		return false, commandCtx.Err()
	}
	message := strings.TrimSpace(result.stdout + "\n" + result.stderr)
	for _, prefix := range []string{"TIMEOUT", "REFUSED", "OTHER:"} {
		if strings.Contains(message, prefix) {
			return false, nil
		}
	}
	return false, errors.New("connectivity probe did not return a recognized network result")
}

func (v verifier) waitForDenial(
	ctx context.Context,
	contextName string,
	namespace string,
	pod string,
	target string,
) (bool, error) {
	deadline := v.now().Add(policyDeadline)
	consecutiveDenials := 0
	for {
		reachable, err := v.probe(ctx, contextName, namespace, pod, target)
		if err != nil {
			return false, err
		}
		if !reachable {
			consecutiveDenials++
			if consecutiveDenials == 2 {
				return true, nil
			}
		} else {
			consecutiveDenials = 0
		}
		if !v.now().Before(deadline) {
			return false, nil
		}
		if err := v.sleep(ctx, policyRetryDelay); err != nil {
			return false, fmt.Errorf("wait for policy convergence: %w", err)
		}
	}
}

func (v verifier) cleanup(
	contextName string,
	namespace string,
	created bool,
	report *Report,
) error {
	if !created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := v.runner.run(ctx, "kubectl", nil,
		"--context", contextName,
		"--request-timeout=8s",
		"delete", "namespace", namespace,
		"--wait=true",
		"--timeout=20s",
	)
	if err != nil {
		report.Checks = append(report.Checks, Check{
			Name: "cleanup", Status: StatusError,
			Message: "the probe namespace could not be confirmed deleted",
		})
		return err
	}
	report.Checks = append(report.Checks, Check{
		Name: "cleanup", Status: StatusPass,
		Message: "the ephemeral probe namespace was deleted",
	})
	return nil
}

func randomNamespace() (string, error) {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "paw-verify-" + hex.EncodeToString(value), nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func namespaceManifest(namespace string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    app.kubernetes.io/name: paw-environment-verifier
    paw.alc.xyz/verification-id: %s
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
    pod-security.kubernetes.io/audit: restricted
    pod-security.kubernetes.io/audit-version: latest
    pod-security.kubernetes.io/warn: restricted
    pod-security.kubernetes.io/warn-version: latest
`, namespace, namespace)
}

func podManifest(namespace string) string {
	return strings.Join([]string{
		probePodManifest(namespace, "egress-client", "paw.alc.xyz/probe-egress: denied", `["pause"]`, false),
		probePodManifest(namespace, "egress-target", "", `["serve-hostname", "--tcp", "--http=false", "--port=8080"]`, true),
		probePodManifest(namespace, "ingress-client", "", `["pause"]`, false),
		probePodManifest(namespace, "ingress-target", "paw.alc.xyz/probe-ingress: denied", `["serve-hostname", "--tcp", "--http=false", "--port=8080"]`, true),
	}, "---\n")
}

func probePodManifest(namespace, name, extraLabel, args string, exposePort bool) string {
	label := ""
	if extraLabel != "" {
		label = "    " + extraLabel + "\n"
	}
	port := ""
	if exposePort {
		port = `      ports:
        - name: http
          containerPort: 8080
`
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
  namespace: %[1]s
  labels:
    app.kubernetes.io/name: paw-environment-verifier
%[3]sspec:
  automountServiceAccountToken: false
  restartPolicy: Never
  terminationGracePeriodSeconds: 1
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: probe
      image: %[4]s
      imagePullPolicy: IfNotPresent
      args: %[5]s
%[6]s      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        privileged: false
        readOnlyRootFilesystem: true
      resources:
        requests:
          cpu: 5m
          memory: 8Mi
        limits:
          cpu: 100m
          memory: 32Mi
`, namespace, name, label, probeImage, args, port)
}

func policyManifest(namespace string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-selected-egress
  namespace: %[1]s
spec:
  podSelector:
    matchLabels:
      paw.alc.xyz/probe-egress: denied
  policyTypes:
    - Egress
  egress: []
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-selected-ingress
  namespace: %[1]s
spec:
  podSelector:
    matchLabels:
      paw.alc.xyz/probe-ingress: denied
  policyTypes:
    - Ingress
  ingress: []
`, namespace)
}
