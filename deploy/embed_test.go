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
