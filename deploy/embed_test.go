package deployment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

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
		ImageReference: reference,
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
