package deployment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestDevelopmentImageMatrix(t *testing.T) {
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
		actual, err := DevelopmentImage(selection)
		if err != nil {
			t.Fatalf("DevelopmentImage(%#v) returned an error: %v", selection, err)
		}
		if actual != expected {
			t.Fatalf("DevelopmentImage(%#v) = %q, want %q", selection, actual, expected)
		}
	}
	if _, err := DevelopmentImage(Selection{Profile: "production", Provider: "codex"}); err == nil {
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
