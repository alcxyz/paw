package environment

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type egressRunner struct {
	changes          map[string]string
	calls            map[string]int
	operationalError bool
}

func (r *egressRunner) run(_ context.Context, _ string, _ io.Reader, args ...string) (commandResult, error) {
	if r.operationalError {
		return commandResult{}, errors.New("exec unavailable")
	}
	pod, script, target := args[6], args[10], args[12]
	key := pod + ":" + script + ":" + target
	r.calls[key]++
	value := "denied"
	if script == connectProbeScript {
		value = "403"
		if target == "probe.example:443" {
			value = "200"
		}
	} else if pod == "pod/egress-positive-control" {
		value = "connected"
		if script == dnsProbeScript {
			value = "resolved"
		}
	}
	if changed, ok := r.changes[key]; ok {
		value = changed
	}
	// A second-call suffix lets tests model controls that stop responding.
	if changed, ok := r.changes[key+":recheck"]; ok && r.calls[key] > 1 {
		value = changed
	}
	return commandResult{stdout: value}, nil
}

func TestEgressQualificationMeasurements(t *testing.T) {
	direct := "pod/workspace-0:" + tcpProbeScript + ":192.0.2.1"
	dns := "pod/workspace-0:" + dnsProbeScript + ":probe.example"
	controlTCP := "pod/egress-positive-control:" + tcpProbeScript + ":192.0.2.1"
	controlDNS := "pod/egress-positive-control:" + dnsProbeScript + ":probe.example"
	for _, test := range []struct {
		name        string
		changes     map[string]string
		want        Outcome
		check       string
		status      Status
		operational bool
	}{
		{name: "enforcing", want: OutcomePass, check: "proxy-workspace-dns", status: StatusPass},
		{name: "tcp bypass", changes: map[string]string{direct: "connected"}, want: OutcomeFail, check: "proxy-direct-bypass", status: StatusFail},
		// TCP remains denied here. A DNS success must independently fail the run.
		{name: "dns open with tcp denied", changes: map[string]string{dns: "resolved"}, want: OutcomeFail, check: "proxy-workspace-dns", status: StatusFail},
		{name: "dns unavailable", changes: map[string]string{controlDNS: "denied"}, want: OutcomeInconclusive, check: "proxy-dns-positive-control", status: StatusInconclusive},
		{name: "tcp unavailable", changes: map[string]string{controlTCP: "denied"}, want: OutcomeInconclusive, check: "proxy-direct-positive-control", status: StatusInconclusive},
		{name: "dns outage during denials", changes: map[string]string{controlDNS + ":recheck": "denied"}, want: OutcomeInconclusive, check: "proxy-dns-positive-recheck", status: StatusInconclusive},
		{name: "tcp outage during denials", changes: map[string]string{controlTCP + ":recheck": "denied"}, want: OutcomeInconclusive, check: "proxy-direct-positive-recheck", status: StatusInconclusive},
		{name: "unlisted admitted", changes: map[string]string{"pod/workspace-0:" + connectProbeScript + ":unlisted.invalid:443": "200"}, want: OutcomeFail, check: "proxy-unlisted-host", status: StatusFail},
		{name: "wrong port admitted", changes: map[string]string{"pod/workspace-0:" + connectProbeScript + ":probe.example:80": "200"}, want: OutcomeFail, check: "proxy-wrong-port", status: StatusFail},
		{name: "allowed refused", changes: map[string]string{"pod/workspace-0:" + connectProbeScript + ":probe.example:443": "403"}, want: OutcomeInconclusive, check: "proxy-allowed-host", status: StatusInconclusive},
		{name: "unexpected output", changes: map[string]string{dns: ""}, want: OutcomeInconclusive, check: "proxy-workspace-dns", status: StatusInconclusive},
		{name: "exec failure", operational: true, want: OutcomeInconclusive, check: "proxy-allowed-host", status: StatusInconclusive},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &egressRunner{changes: test.changes, calls: map[string]int{}, operationalError: test.operational}
			report := Report{Outcome: OutcomePass, ProviderAvailability: "unavailable", EgressQualification: "unproven"}
			err := testVerifier(runner).qualifyEgress(context.Background(), Request{Context: "fixture", Egress: &EgressRequest{Namespace: "fixture", AllowedHost: "probe.example", DirectAddress: "192.0.2.1"}}, &report)
			if err != nil || report.Outcome != test.want || !hasCheck(report, test.check, test.status) {
				t.Fatalf("report=%+v, error=%v", report, err)
			}
			if report.ProviderAvailability != "unavailable" {
				t.Fatal("transport probes enabled a provider")
			}
			if test.want == OutcomePass && (report.EgressQualification != "pass" || len(report.Checks) != 9) {
				t.Fatalf("missing qualification checks: %+v", report)
			}
		})
	}
}

func TestDNSProbeMeasuresResolutionWithoutTCP(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		exit    string
		want    string
		success bool
	}{
		{"resolved", "0", "resolved", true}, {"not found", "2", "denied", true}, {"timeout", "124", "denied", true}, {"operational failure", "3", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"timeout": "shift\nexec \"$@\"\n",
				"getent":  "test \"$1\" = ahostsv4 || exit 99\ntest \"$2\" = probe.example || exit 99\nexit " + test.exit + "\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+bash+"\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, "-ceu", dnsProbeScript, "fixture", "probe.example")
			cmd.Env = []string{"PATH=" + dir}
			out, err := cmd.CombinedOutput()
			if (err == nil) != test.success || strings.TrimSpace(string(out)) != test.want {
				t.Fatalf("output=%q, err=%v", out, err)
			}
		})
	}
	// No resolver binary is an execution error, not DNS denial.
	cmd := exec.Command(bash, "-ceu", dnsProbeScript, "fixture", "probe.example")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	if out, err := cmd.CombinedOutput(); err == nil || len(out) > 0 {
		t.Fatalf("missing tools: %q, %v", out, err)
	}
}

type qualificationRunner struct {
	baseline probeRunner
	egress   egressRunner
}

func (r *qualificationRunner) run(ctx context.Context, name string, stdin io.Reader, args ...string) (commandResult, error) {
	if len(args) > 10 && (args[10] == tcpProbeScript || args[10] == dnsProbeScript || args[10] == connectProbeScript) {
		return r.egress.run(ctx, name, stdin, args...)
	}
	return r.baseline.run(ctx, name, stdin, args...)
}

func TestVerifyKeepsQualificationSeparateAndCleansUp(t *testing.T) {
	baseline := &probeRunner{networkEnforced: true}
	report, err := testVerifier(baseline).verify(context.Background(), Request{Context: "fixture"})
	if err != nil || report.Outcome != OutcomePass || report.EgressQualification != "unproven" || report.ProviderAvailability != "unavailable" {
		t.Fatalf("baseline: %+v, %v", report, err)
	}
	runner := &qualificationRunner{baseline: probeRunner{networkEnforced: true}, egress: egressRunner{calls: map[string]int{}}}
	report, err = testVerifier(runner).verify(context.Background(), Request{Context: "fixture", Egress: &EgressRequest{Namespace: "fixture", AllowedHost: "probe.example", DirectAddress: "192.0.2.1"}})
	if err != nil || report.Outcome != OutcomePass || report.EgressQualification != "pass" || report.ProviderAvailability != "unavailable" || !runner.baseline.deletedNamespace() {
		t.Fatalf("qualification: %+v, %v", report, err)
	}
	for _, request := range []EgressRequest{
		{Namespace: "-bad", AllowedHost: "probe.example", DirectAddress: "192.0.2.1"},
		{Namespace: "fixture", AllowedHost: "probe..example", DirectAddress: "192.0.2.1"},
		{Namespace: "fixture", AllowedHost: "192.0.2.1", DirectAddress: "192.0.2.1"},
		{Namespace: "fixture", AllowedHost: "probe.example", DirectAddress: "probe.example"},
	} {
		runner := &probeRunner{}
		_, err := testVerifier(runner).verify(context.Background(), Request{Context: "fixture", Egress: &request})
		if err == nil || len(runner.calls) != 0 {
			t.Fatalf("invalid input touched cluster: %+v, %v", request, err)
		}
	}
}

func TestVerifyCollectsNegativeLaneWithoutLosingBaselineFailure(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		changes := map[string]string{}
		if bypass {
			changes["pod/workspace-0:"+tcpProbeScript+":192.0.2.1"] = "connected"
		}
		runner := &qualificationRunner{baseline: probeRunner{networkEnforced: false}, egress: egressRunner{changes: changes, calls: map[string]int{}}}
		report, err := testVerifier(runner).verify(context.Background(), Request{Context: "fixture", Egress: &EgressRequest{Namespace: "fixture", AllowedHost: "probe.example", DirectAddress: "192.0.2.1"}})
		if err != nil || report.Outcome != OutcomeFail || report.EgressQualification != "fail" || !runner.baseline.deletedNamespace() {
			t.Fatalf("negative lane: %+v, %v", report, err)
		}
		want := StatusPass
		if bypass {
			want = StatusFail
		}
		if !hasCheck(report, "proxy-direct-bypass", want) {
			t.Fatalf("missing direct bypass diagnosis: %+v", report)
		}
	}
}
