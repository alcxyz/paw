// Package deployment embeds PAW's canonical Kubernetes resources for the CLI.
package deployment

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

//go:embed base/*.yaml adapters/*/*.yaml
var manifests embed.FS

const (
	// AdapterKubernetes is the portable lifecycle implemented through standard
	// Kubernetes resources and an explicitly selected context.
	AdapterKubernetes = "kubernetes"
	// AdapterMinikube retains local development image behavior as a
	// compatibility and reference-environment adapter.
	AdapterMinikube = "minikube"
)

var (
	immutableDigest     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	immutableRepository = regexp.MustCompile(
		`^(?:localhost|[a-z0-9]+(?:[.-][a-z0-9]+)*)(?::[0-9]+)?` +
			`(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`,
	)
)

// Selection is the immutable profile/provider composition requested by an
// operator. Values are resolved through the reviewed image matrix below; they
// are never interpreted as arbitrary image names.
type Selection struct {
	Profile  string
	Provider string
}

// ManifestRequest selects an adapter, reviewed image composition, and optional
// immutable released image. Only the generic Kubernetes adapter accepts an
// ImageReference; Minikube binds the corresponding local development image.
type ManifestRequest struct {
	Adapter        string
	Selection      Selection
	ImageReference string
	// EgressImageReference is the immutable egress proxy image for the generic
	// Kubernetes adapter. Minikube binds the local paw-egress-proxy:dev image.
	EgressImageReference string
}

// EgressImageName is the reviewed image name of the per-workspace egress proxy.
const EgressImageName = "paw-egress-proxy"

var imageNames = map[Selection]string{
	{Profile: "core", Provider: "none"}:                     "paw-core",
	{Profile: "core", Provider: "codex"}:                    "paw-codex",
	{Profile: "core", Provider: "claude-code"}:              "paw-claude-code",
	{Profile: "core", Provider: "opencode"}:                 "paw-opencode",
	{Profile: "developer", Provider: "none"}:                "paw-developer",
	{Profile: "developer", Provider: "codex"}:               "paw-developer-codex",
	{Profile: "developer", Provider: "claude-code"}:         "paw-developer-claude-code",
	{Profile: "developer", Provider: "opencode"}:            "paw-developer-opencode",
	{Profile: "platform-readonly", Provider: "none"}:        "paw-platform-readonly",
	{Profile: "platform-readonly", Provider: "codex"}:       "paw-platform-readonly-codex",
	{Profile: "platform-readonly", Provider: "claude-code"}: "paw-platform-readonly-claude-code",
	{Profile: "platform-readonly", Provider: "opencode"}:    "paw-platform-readonly-opencode",
	// "all" composes every reviewed provider into one image so one T3 can
	// offer them side by side; single-provider images remain the lean choice.
	{Profile: "core", Provider: "all"}:              "paw-all",
	{Profile: "developer", Provider: "all"}:         "paw-developer-all",
	{Profile: "platform-readonly", Provider: "all"}: "paw-platform-readonly-all",
}

// ImageName returns the reviewed image name for a profile/provider selection.
func ImageName(selection Selection) (string, error) {
	image, exists := imageNames[selection]
	if !exists {
		return "", fmt.Errorf(
			"unsupported profile/provider selection %q/%q",
			selection.Profile,
			selection.Provider,
		)
	}
	return image, nil
}

// SupportsAdapter reports whether the CLI implements the named lifecycle
// adapter. It makes no claim that the selected cluster conforms to PAW.
func SupportsAdapter(adapter string) bool {
	return adapter == AdapterKubernetes || adapter == AdapterMinikube
}

// ValidateReleasedImageReference validates an immutable OCI image reference
// against the reviewed image composition selected by the profile and provider.
// It returns the repository and digest in the form expected by Kustomize.
func ValidateReleasedImageReference(selection Selection, reference string) (string, string, error) {
	expected, err := ImageName(selection)
	if err != nil {
		return "", "", err
	}
	return validateImmutableReference(expected, reference)
}

// ValidateEgressImageReference validates an immutable OCI image reference for
// the per-workspace egress proxy image.
func ValidateEgressImageReference(reference string) (string, string, error) {
	return validateImmutableReference(EgressImageName, reference)
}

func validateImmutableReference(expected, reference string) (string, string, error) {
	if strings.ContainsAny(reference, " \t\r\n") || strings.Contains(reference, "://") {
		return "", "", fmt.Errorf("released image must be an OCI reference without a URL scheme")
	}

	repository, digest, found := strings.Cut(reference, "@")
	if !found || repository == "" || !immutableDigest.MatchString(digest) || strings.Contains(digest, "@") {
		return "", "", fmt.Errorf("released image must use NAME@sha256:<64 lowercase hex characters>")
	}
	if !immutableRepository.MatchString(repository) {
		return "", "", fmt.Errorf("released image must use a lowercase fully qualified repository")
	}
	basename := repository[strings.LastIndex(repository, "/")+1:]
	if strings.Contains(basename, ":") {
		return "", "", fmt.Errorf("released image must not include a mutable tag")
	}
	if basename != expected {
		return "", "", fmt.Errorf(
			"released image %q does not match profile/provider image %q",
			basename,
			expected,
		)
	}
	return repository, digest, nil
}

// Materialize writes the embedded resources to an isolated temporary directory.
// The returned cleanup function only removes that directory.
func Materialize(adapter string) (string, func(), error) {
	return MaterializeSelection(adapter, Selection{Profile: "core", Provider: "none"})
}

// MaterializeSelection writes the embedded resources and binds one reviewed
// profile/provider image composition to the selected adapter.
func MaterializeSelection(adapter string, selection Selection) (string, func(), error) {
	return MaterializeManifest(ManifestRequest{Adapter: adapter, Selection: selection})
}

// MaterializeManifest writes the embedded resources and binds one reviewed
// profile/provider image composition to the requested adapter.
func MaterializeManifest(request ManifestRequest) (string, func(), error) {
	if !SupportsAdapter(request.Adapter) {
		return "", func() {}, fmt.Errorf("unsupported adapter %q", request.Adapter)
	}
	if request.Adapter == AdapterMinikube && (request.ImageReference != "" || request.EgressImageReference != "") {
		return "", func() {}, fmt.Errorf("adapter %q does not accept a released image", request.Adapter)
	}
	if request.Adapter == AdapterKubernetes && request.ImageReference != "" && request.EgressImageReference == "" {
		return "", func() {}, fmt.Errorf("adapter %q requires a released egress proxy image", request.Adapter)
	}

	selection := request.Selection
	image, err := ImageName(selection)
	if err != nil {
		return "", func() {}, err
	}
	destinations, err := EgressDestinations(selection)
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

	adapterPath := filepath.Join(root, "adapters", request.Adapter)
	kustomizationPath := filepath.Join(adapterPath, "kustomization.yaml")
	kustomization, err := os.ReadFile(kustomizationPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("read adapter kustomization: %w", err)
	}
	configured := string(kustomization)
	if request.Adapter == AdapterMinikube {
		configured = strings.Replace(configured, "newName: paw-core", "newName: "+image, 1)
		if configured == string(kustomization) && image != "paw-core" {
			cleanup()
			return "", func() {}, fmt.Errorf("adapter image placeholder is missing")
		}
	}
	if request.ImageReference != "" {
		repository, digest, validationErr := ValidateReleasedImageReference(selection, request.ImageReference)
		if validationErr != nil {
			cleanup()
			return "", func() {}, validationErr
		}
		egressRepository, egressDigest, validationErr := ValidateEgressImageReference(request.EgressImageReference)
		if validationErr != nil {
			cleanup()
			return "", func() {}, validationErr
		}
		configured += fmt.Sprintf(`images:
  - name: registry.invalid/paw/workspace
    newName: %s
    digest: %s
  - name: registry.invalid/paw/egress-proxy
    newName: %s
    digest: %s
`, repository, digest, egressRepository, egressDigest)
	}
	// Minikube already declares its local-only image-pull patch. Append the
	// composition patches to that list rather than emitting a duplicate key.
	if request.Adapter != AdapterMinikube {
		configured += "patches:\n"
	}
	configured += fmt.Sprintf(`  - target:
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
  - target:
      version: v1
      kind: ConfigMap
      name: egress-destinations
    patch: |-
      - op: replace
        path: /data/destinations
        value: %s
`, selection.Profile, selection.Provider, selection.Profile, selection.Provider,
		selection.Profile, selection.Provider, strconv.Quote(EgressDestinationsText(destinations)))
	if err := os.WriteFile(kustomizationPath, []byte(configured), 0o644); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write adapter kustomization: %w", err)
	}

	return adapterPath, cleanup, nil
}
