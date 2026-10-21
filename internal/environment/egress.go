package environment

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// EgressRequest selects an already prepared disposable workspace and an
// unselected positive-control pod in the same namespace. Verification only
// executes probes; the caller owns preparation and cleanup of these resources.
type EgressRequest struct {
	Namespace     string `json:"namespace"`
	AllowedHost   string `json:"allowedHost"`
	DirectAddress string `json:"directAddress"`
}

var probeNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var probeHostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// These scripts print only a recognized measurement. Missing tools, remote
// execution failures and unexpected output remain inconclusive, never denial.
const tcpProbeScript = `command -v timeout >/dev/null
command -v bash >/dev/null
if timeout 5 bash -c '</dev/tcp/$1/443' paw-tcp "$1" 2>/dev/null; then
 printf connected
else
 result=$?
 test "$result" = 124 || exit 3
 printf denied
fi`

// getent only resolves the name. It never opens an application TCP connection.
const dnsProbeScript = `command -v timeout >/dev/null
command -v getent >/dev/null
if timeout 5 getent ahostsv4 "$1" >/dev/null 2>&1; then
 printf resolved
else
 result=$?
 test "$result" = 2 || test "$result" = 124 || exit 3
 printf denied
fi`

const connectProbeScript = `command -v timeout >/dev/null
command -v bash >/dev/null
timeout 12 bash -ceu '
 test -n "${EGRESS_SERVICE_HOST:-}"
 exec 3<>"/dev/tcp/$EGRESS_SERVICE_HOST/${EGRESS_SERVICE_PORT:-3128}"
 printf "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n" "$1" "$1" >&3
 IFS= read -r -t 10 line <&3
 case "$line" in
  "HTTP/1.1 200 "*) printf 200 ;;
  "HTTP/1.1 403 "*) printf 403 ;;
  *) exit 3 ;;
 esac
' paw-connect "$1"`

func (v verifier) egressMeasurement(ctx context.Context, contextName, namespace, pod, script, target string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second+apiTimeout)
	defer cancel()
	result, err := v.runner.run(commandCtx, "kubectl", nil,
		"--context", contextName, "--namespace", namespace, "--request-timeout=8s",
		"exec", "pod/"+pod, "--", "bash", "-ceu", script, "paw-egress-probe", target)
	if err != nil {
		return "", errors.New("egress probe did not complete")
	}
	return strings.TrimSpace(result.stdout), nil
}

// qualifyEgress measures ADR-013 decision 4 transport behavior. It cannot
// establish application routing within an end-to-end encrypted tunnel and
// never enables provider profiles.
func (v verifier) qualifyEgress(ctx context.Context, request Request, report *Report) error {
	selected := request.Egress
	if selected == nil {
		return nil
	}
	if err := validateEgressRequest(*selected); err != nil {
		return err
	}
	selection := *selected
	report.EgressProbe = &selection
	report.ProbeVersion = ProbeVersion + "+proxy-v1"
	failed, inconclusive := false, false
	measure := func(name, pod, script, target, want string, negative bool) bool {
		value, err := v.egressMeasurement(ctx, request.Context, selected.Namespace, pod, script, target)
		check := Check{Name: name, Status: StatusPass, Message: "observed " + want}
		if err != nil || (script == connectProbeScript && value != "200" && value != "403") || (script == tcpProbeScript && value != "connected" && value != "denied") || (script == dnsProbeScript && value != "resolved" && value != "denied") {
			check.Status, check.Message = StatusInconclusive, "probe could not produce a recognized measurement"
			inconclusive = true
		} else if value != want {
			check.Status, check.Message = StatusInconclusive, "positive control did not succeed"
			inconclusive = true
			if negative {
				check.Status, check.Message = StatusFail, "required refusal was not enforced"
				failed = true
			}
		}
		report.Checks = append(report.Checks, check)
		return check.Status == StatusPass
	}
	// Controls bracket the denials so an unavailable target or resolver cannot
	// be mistaken for effective policy. No provider endpoints or logins are used.
	tcpReady := measure("proxy-direct-positive-control", "egress-positive-control", tcpProbeScript, selected.DirectAddress, "connected", false)
	dnsReady := measure("proxy-dns-positive-control", "egress-positive-control", dnsProbeScript, selected.AllowedHost, "resolved", false)
	measure("proxy-allowed-host", "workspace-0", connectProbeScript, selected.AllowedHost+":443", "200", false)
	unlisted := "unlisted.invalid"
	if selected.AllowedHost == unlisted {
		unlisted = "other.invalid"
	}
	measure("proxy-unlisted-host", "workspace-0", connectProbeScript, unlisted+":443", "403", true)
	measure("proxy-wrong-port", "workspace-0", connectProbeScript, selected.AllowedHost+":80", "403", true)
	if tcpReady {
		measure("proxy-direct-bypass", "workspace-0", tcpProbeScript, selected.DirectAddress, "denied", true)
	}
	if dnsReady {
		measure("proxy-workspace-dns", "workspace-0", dnsProbeScript, selected.AllowedHost, "denied", true)
	}
	measure("proxy-direct-positive-recheck", "egress-positive-control", tcpProbeScript, selected.DirectAddress, "connected", false)
	measure("proxy-dns-positive-recheck", "egress-positive-control", dnsProbeScript, selected.AllowedHost, "resolved", false)
	switch {
	case failed:
		report.EgressQualification = "fail"
		report.Outcome = OutcomeFail
	case inconclusive:
		report.EgressQualification = "inconclusive"
		report.Outcome = OutcomeInconclusive
	default:
		report.EgressQualification = "pass"
	}
	return nil
}

func validateEgressRequest(selected EgressRequest) error {
	if !probeNamespace.MatchString(selected.Namespace) || !probeHostname.MatchString(selected.AllowedHost) || !strings.Contains(selected.AllowedHost, ".") {
		return errors.New("egress verification requires a valid disposable namespace and allowed hostname")
	}
	for _, label := range strings.Split(selected.AllowedHost, ".") {
		if !probeNamespace.MatchString(label) {
			return errors.New("egress allowed host contains an invalid hostname label")
		}
	}
	if _, err := netip.ParseAddr(selected.DirectAddress); err != nil {
		return errors.New("egress direct address must be an IP literal")
	}
	if _, err := netip.ParseAddr(selected.AllowedHost); err == nil {
		return errors.New("egress allowed host must be a hostname, not an IP literal")
	}
	return nil
}
