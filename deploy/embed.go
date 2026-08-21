// Package deployment embeds PAW's canonical Kubernetes resources for the CLI.
package deployment

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed base/*.yaml adapters/minikube/*.yaml
var manifests embed.FS

// Materialize writes the embedded resources to an isolated temporary directory.
// The returned cleanup function only removes that directory.
func Materialize(adapter string) (string, func(), error) {
	if adapter != "minikube" {
		return "", func() {}, fmt.Errorf("unsupported adapter %q", adapter)
	}

	root, err := os.MkdirTemp("", "paw-manifests-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create manifest directory: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(root)
	}

	err = fs.WalkDir(manifests, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}

		target := filepath.Join(root, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		content, readErr := manifests.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if writeErr := os.WriteFile(target, content, 0o644); writeErr != nil {
			return writeErr
		}
		return nil
	})
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("materialize manifests: %w", err)
	}

	return filepath.Join(root, "adapters", adapter), cleanup, nil
}
