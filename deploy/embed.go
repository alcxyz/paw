// Package deployment embeds PAW's canonical Kubernetes resources for the CLI.
package deployment

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed base/*.yaml adapters/minikube/*.yaml
var manifests embed.FS

// Selection is the immutable profile/provider composition requested by an
// operator. Values are resolved through the reviewed image matrix below; they
// are never interpreted as arbitrary image names.
type Selection struct {
	Profile  string
	Provider string
}

var developmentImages = map[Selection]string{
	{Profile: "core", Provider: "none"}:                     "paw-core",
	{Profile: "core", Provider: "codex"}:                    "paw-codex",
	{Profile: "core", Provider: "claude-code"}:              "paw-claude-code",
	{Profile: "core", Provider: "opencode"}:                 "paw-opencode",
	{Profile: "platform-readonly", Provider: "none"}:        "paw-platform-readonly",
	{Profile: "platform-readonly", Provider: "codex"}:       "paw-platform-readonly-codex",
	{Profile: "platform-readonly", Provider: "claude-code"}: "paw-platform-readonly-claude-code",
	{Profile: "platform-readonly", Provider: "opencode"}:    "paw-platform-readonly-opencode",
}

// DevelopmentImage returns the reviewed local image name for a selection.
func DevelopmentImage(selection Selection) (string, error) {
	image, exists := developmentImages[selection]
	if !exists {
		return "", fmt.Errorf(
			"unsupported profile/provider selection %q/%q",
			selection.Profile,
			selection.Provider,
		)
	}
	return image, nil
}

// Materialize writes the embedded resources to an isolated temporary directory.
// The returned cleanup function only removes that directory.
func Materialize(adapter string) (string, func(), error) {
	return MaterializeSelection(adapter, Selection{Profile: "core", Provider: "none"})
}

// MaterializeSelection writes the embedded resources and binds one reviewed
// profile/provider image composition to the selected adapter.
func MaterializeSelection(adapter string, selection Selection) (string, func(), error) {
	if adapter != "minikube" {
		return "", func() {}, fmt.Errorf("unsupported adapter %q", adapter)
	}
	image, err := DevelopmentImage(selection)
	if err != nil {
		return "", func() {}, err
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

	adapterPath := filepath.Join(root, "adapters", adapter)
	kustomizationPath := filepath.Join(adapterPath, "kustomization.yaml")
	kustomization, err := os.ReadFile(kustomizationPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("read adapter kustomization: %w", err)
	}
	configured := strings.Replace(
		string(kustomization),
		"newName: paw-core",
		"newName: "+image,
		1,
	)
	if configured == string(kustomization) && image != "paw-core" {
		cleanup()
		return "", func() {}, fmt.Errorf("adapter image placeholder is missing")
	}
	configured += fmt.Sprintf(`patches:
  - target:
      group: apps
      version: v1
      kind: StatefulSet
      name: workspace
    patch: |-
      - op: replace
        path: /metadata/annotations/paw.alc.xyz~1profile
        value: %s
      - op: replace
        path: /metadata/annotations/paw.alc.xyz~1provider
        value: %s
      - op: replace
        path: /spec/template/metadata/annotations/paw.alc.xyz~1profile
        value: %s
      - op: replace
        path: /spec/template/metadata/annotations/paw.alc.xyz~1provider
        value: %s
  - target:
      version: v1
      kind: ConfigMap
      name: workspace-contract
    patch: |-
      - op: replace
        path: /data/profile
        value: %s
      - op: replace
        path: /data/provider
        value: %s
`, selection.Profile, selection.Provider, selection.Profile, selection.Provider,
		selection.Profile, selection.Provider)
	if err := os.WriteFile(kustomizationPath, []byte(configured), 0o644); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write adapter kustomization: %w", err)
	}

	return adapterPath, cleanup, nil
}
