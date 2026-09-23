package deployment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testEgressDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestMaterializeMinikube(t *testing.T) {
	path, cleanup, err := Materialize("minikube")
	if err != nil {
		t.Fatalf("Materialize returned an error: %v", err)
	}
	root := filepath.Clean(filepath.Join(path, "..", ".."))
	cleanup()

	if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove materialized resources: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(root), "paw-manifests-") {
		t.Fatalf("unexpected temporary root %q", root)
	}
}

func TestMaterializeRejectsUnknownAdapter(t *testing.T) {
	if _, _, err := Materialize("unknown"); err == nil {
		t.Fatal("expected unsupported adapter error")
	}
}

func TestMaterializeIncludesPersistentWorkspaceStorageLayout(t *testing.T) {
	path, cleanup, err := Materialize("minikube")
	if err != nil {
		t.Fatalf("Materialize returned an error: %v", err)
	}
	defer cleanup()

	base := filepath.Join(path, "..", "..", "base")
	checks := map[string][]string{
		"configmap.yaml": {
			"state-policy: retain-until-destroy",
		},
		"pvc.yaml": {
			"name: workspace-state",
			"name: workspace-work",
			"name: workspace-session",
			"paw.alc.xyz/managed-by: paw",
			"paw.alc.xyz/state-policy: retain-until-destroy",
			"storage: 10Gi",
		},
		"statefulset.yaml": {
			"paw.alc.xyz/storage-layout: persistent-v2",
			"claimName: workspace-state",
			"claimName: workspace-work",
			"claimName: workspace-session",
			"mountPath: /tmp",
		},
	}

	for name, expectedStrings := range checks {
		content, readErr := os.ReadFile(filepath.Join(base, name))
		if readErr != nil {
			t.Fatalf("read materialized %s: %v", name, readErr)
		}
		for _, expected := range expectedStrings {
			if !strings.Contains(string(content), expected) {
				t.Fatalf("materialized %s does not contain %q:\n%s", name, expected, content)
			}
		}
	}
}

func TestSupportedAdapters(t *testing.T) {
	for _, adapter := range []string{AdapterKubernetes, AdapterMinikube} {
		if !SupportsAdapter(adapter) {
			t.Fatalf("expected adapter %q to be supported", adapter)
		}
	}
	if SupportsAdapter("calico") {
		t.Fatal("networking implementation must not be a lifecycle adapter")
	}
}

func TestImageNameMatrix(t *testing.T) {
	tests := map[Selection]string{
		{Profile: "core", Provider: "none"}:                     "paw-core",
		{Profile: "core", Provider: "codex"}:                    "paw-codex",
		{Profile: "core", Provider: "claude-code"}:              "paw-claude-code",
		{Profile: "core", Provider: "opencode"}:                 "paw-opencode",
		{Profile: "platform-readonly", Provider: "none"}:        "paw-platform-readonly",
		{Profile: "platform-readonly", Provider: "codex"}:       "paw-platform-readonly-codex",
		{Profile: "platform-readonly", Provider: "claude-code"}: "paw-platform-readonly-claude-code",
		{Profile: "platform-readonly", Provider: "opencode"}:    "paw-platform-readonly-opencode",
	}

	for selection, expected := range tests {
		actual, err := ImageName(selection)
		if err != nil {
			t.Fatalf("ImageName(%#v) returned an error: %v", selection, err)
		}
		if actual != expected {
			t.Fatalf("ImageName(%#v) = %q, want %q", selection, actual, expected)
		}
	}
	if _, err := ImageName(Selection{Profile: "production", Provider: "codex"}); err == nil {
		t.Fatal("expected unknown selection to fail")
	}
}

func TestMaterializeSelectionBindsImageAndMetadata(t *testing.T) {
	path, cleanup, err := MaterializeSelection(
		"minikube",
		Selection{Profile: "platform-readonly", Provider: "opencode"},
	)
	if err != nil {
		t.Fatalf("MaterializeSelection returned an error: %v", err)
	}
	defer cleanup()

	content, err := os.ReadFile(filepath.Join(path, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read kustomization: %v", err)
	}
	for _, expected := range []string{
		"newName: paw-platform-readonly-opencode",
		"path: /data/profile",
		"value: platform-readonly",
		"path: /data/provider",
		"value: opencode",
	} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("kustomization does not contain %q:\n%s", expected, content)
		}
	}
}

func TestMaterializeGenericKubernetesBindsImmutableReleasedImage(t *testing.T) {
	reference := "registry.example/paw/paw-platform-readonly-opencode@" + testDigest
	path, cleanup, err := MaterializeManifest(ManifestRequest{
		Adapter: AdapterKubernetes,
		Selection: Selection{
			Profile:  "platform-readonly",
			Provider: "opencode",
		},
		ImageReference:       reference,
		EgressImageReference: "registry.example/paw/paw-egress-proxy@" + testEgressDigest,
	})
	if err != nil {
		t.Fatalf("MaterializeManifest returned an error: %v", err)
	}
	defer cleanup()

	content, err := os.ReadFile(filepath.Join(path, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read kustomization: %v", err)
	}
	for _, expected := range []string{
		"newName: registry.example/paw/paw-platform-readonly-opencode",
		"digest: " + testDigest,
		"newName: registry.example/paw/paw-egress-proxy",
		"digest: " + testEgressDigest,
		"value: platform-readonly",
		"value: opencode",
	} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("generic kustomization does not contain %q:\n%s", expected, content)
		}
	}
	for _, forbidden := range []string{"minikube", "newTag: dev", ":dev"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("generic kustomization contains Minikube assumption %q:\n%s", forbidden, content)
		}
	}
}

func TestValidateReleasedImageReference(t *testing.T) {
	selection := Selection{Profile: "core", Provider: "codex"}
	valid := "registry.example/team/paw-codex@" + testDigest
	repository, digest, err := ValidateReleasedImageReference(selection, valid)
	if err != nil {
		t.Fatalf("valid image was rejected: %v", err)
	}
	if repository != "registry.example/team/paw-codex" || digest != testDigest {
		t.Fatalf("unexpected parsed image: repository=%q digest=%q", repository, digest)
	}

	for _, reference := range []string{
		"paw-codex@" + testDigest,
		"registry.example/team/paw-codex:latest",
		"registry.example/team/paw-codex:latest@" + testDigest,
		"registry.example/team/paw-core@" + testDigest,
		"https://registry.example/team/paw-codex@" + testDigest,
		"registry.example/team/paw-codex#fragment@" + testDigest,
		"registry.example/team/paw-codex@sha256:ABCDEF",
	} {
		if _, _, err := ValidateReleasedImageReference(selection, reference); err == nil {
			t.Fatalf("expected invalid image %q to be rejected", reference)
		}
	}
}

func TestMinikubeRejectsReleasedImageReference(t *testing.T) {
	_, cleanup, err := MaterializeManifest(ManifestRequest{
		Adapter:        AdapterMinikube,
		Selection:      Selection{Profile: "core", Provider: "none"},
		ImageReference: "registry.example/paw/paw-core@" + testDigest,
	})
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("expected Minikube released-image rejection, got %v", err)
	}
}

func TestMaterializeGenericKubernetesRequiresEgressImage(t *testing.T) {
	_, _, err := MaterializeManifest(ManifestRequest{
		Adapter:        AdapterKubernetes,
		Selection:      Selection{Profile: "core", Provider: "codex"},
		ImageReference: "registry.example/paw/paw-codex@" + testDigest,
	})
	if err == nil {
		t.Fatal("expected an error without a released egress proxy image")
	}
	_, _, err = MaterializeManifest(ManifestRequest{
		Adapter:              AdapterKubernetes,
		Selection:            Selection{Profile: "core", Provider: "codex"},
		ImageReference:       "registry.example/paw/paw-codex@" + testDigest,
		EgressImageReference: "registry.example/paw/paw-codex@" + testEgressDigest,
	})
	if err == nil {
		t.Fatal("expected an error for an egress reference naming another image")
	}
}

func TestMinikubeRejectsEgressImageReference(t *testing.T) {
	_, _, err := MaterializeManifest(ManifestRequest{
		Adapter:              AdapterMinikube,
		Selection:            Selection{Profile: "core", Provider: "none"},
		EgressImageReference: "registry.example/paw/paw-egress-proxy@" + testEgressDigest,
	})
	if err == nil {
		t.Fatal("expected minikube to reject a released egress image")
	}
}

func TestEgressDestinationsResolveByProvider(t *testing.T) {
	destinations, err := EgressDestinations(Selection{Profile: "core", Provider: "codex"})
	if err != nil {
		t.Fatalf("EgressDestinations returned an error: %v", err)
	}
	hosts := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		if destination.Purpose != PurposeApprovedProviderAPI {
			t.Fatalf("unexpected purpose %q for %s", destination.Purpose, destination.Host)
		}
		hosts = append(hosts, destination.Host)
	}
	if strings.Join(hosts, ",") != "auth.openai.com,chatgpt.com,api.openai.com" {
		t.Fatalf("unexpected codex destinations %v", hosts)
	}
	claude, err := EgressDestinations(Selection{Profile: "core", Provider: "claude-code"})
	if err != nil {
		t.Fatalf("EgressDestinations returned an error: %v", err)
	}
	claudeHosts := make([]string, 0, len(claude))
	for _, destination := range claude {
		claudeHosts = append(claudeHosts, destination.Host)
	}
	if strings.Join(claudeHosts, ",") != "claude.ai,platform.claude.com,api.anthropic.com" {
		t.Fatalf("unexpected claude-code destinations %v", claudeHosts)
	}
	none, err := EgressDestinations(Selection{Profile: "core", Provider: "none"})
	if err != nil || len(none) != 0 {
		t.Fatalf("provider none should resolve to no destinations, got %v, %v", none, err)
	}
	if _, err := EgressDestinations(Selection{Profile: "core", Provider: "unknown"}); err == nil {
		t.Fatal("expected an unknown provider to be rejected")
	}
	text := EgressDestinationsText(destinations)
	if text != "auth.openai.com approved-provider-api\nchatgpt.com approved-provider-api\napi.openai.com approved-provider-api\n" {
		t.Fatalf("unexpected destination text %q", text)
	}
}

func TestMaterializeSelectionRendersDestinations(t *testing.T) {
	path, cleanup, err := MaterializeSelection(AdapterMinikube, Selection{Profile: "core", Provider: "codex"})
	if err != nil {
		t.Fatalf("MaterializeSelection returned an error: %v", err)
	}
	defer cleanup()
	content, err := os.ReadFile(filepath.Join(path, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("read kustomization: %v", err)
	}
	for _, expected := range []string{
		"name: egress-destinations",
		"path: /data/destinations",
		`value: "auth.openai.com approved-provider-api\nchatgpt.com approved-provider-api\napi.openai.com approved-provider-api\n"`,
		"newName: paw-egress-proxy",
	} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("kustomization does not contain %q:\n%s", expected, content)
		}
	}
}
