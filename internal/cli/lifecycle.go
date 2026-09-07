package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const workspaceNamespace = "paw-workspace"
const workspaceOwnerLabel = "paw.alc.xyz/managed-by"

func runWorkspaceCreate(options workspaceOptions, manifestPath string, stdout, stderr io.Writer, deps dependencies) int {
	if exit := runEnvironment([]string{"verify", "--context", options.context}, stderr, stderr, deps); exit != 0 {
		fmt.Fprintln(stderr, "paw: workspace creation requires passing network verification")
		return exit
	}
	// Creation, unlike apply, atomically refuses a namespace that already exists.
	// Both adapters share this embedded namespace manifest.
	namespacePath := filepath.Join(manifestPath, "..", "kubernetes", "namespace.yaml")
	if err := deps.runCommand("kubectl", []string{
		"--context", options.context, "--request-timeout=10s",
		"create", "-f", namespacePath,
	}, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: create workspace namespace: %v; no workspace resources were applied; inspect the namespace before retrying\n", err)
		return 1
	}
	for _, args := range [][]string{
		{"--context", options.context, "--request-timeout=10s", "apply", "-k", manifestPath},
		{"--context", options.context, "--namespace", workspaceNamespace,
			"--request-timeout=130s", "rollout", "status", "statefulset/workspace", "--timeout=120s"},
	} {
		if err := deps.runCommand("kubectl", args, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "paw: workspace creation did not complete: %v; resources are retained for inspection; check storage, image availability, and pod readiness, or use workspace destroy --delete-state on the same context\n", err)
			return 1
		}
	}
	return 0
}

func runWorkspaceDestroy(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	var output bytes.Buffer
	if err := deps.runCommand("kubectl", []string{
		"--context", options.context, "--request-timeout=10s",
		"get", "namespace", workspaceNamespace, "--ignore-not-found=true", "--output=json",
	}, &output, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: inspect workspace ownership: %v\n", err)
		return 1
	}
	if len(bytes.TrimSpace(output.Bytes())) == 0 {
		fmt.Fprintln(stdout, "workspace namespace is already absent")
		return 0
	}
	var namespace struct {
		Metadata struct {
			Name            string            `json:"name"`
			UID             string            `json:"uid"`
			ResourceVersion string            `json:"resourceVersion"`
			Labels          map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(output.Bytes(), &namespace); err != nil {
		fmt.Fprintln(stderr, "paw: invalid workspace ownership response; refusing deletion")
		return 1
	}
	metadata := namespace.Metadata
	if metadata.Name != workspaceNamespace || metadata.UID == "" || metadata.ResourceVersion == "" || metadata.Labels[workspaceOwnerLabel] != "paw" {
		fmt.Fprintln(stderr, "paw: namespace is not identified as a PAW-managed workspace; refusing deletion")
		return 1
	}
	// Preconditions bind deletion to the object and ownership revision inspected
	// above. A label check followed by ordinary kubectl delete has a race.
	request := struct {
		APIVersion    string            `json:"apiVersion"`
		Kind          string            `json:"kind"`
		Preconditions map[string]string `json:"preconditions"`
	}{"v1", "DeleteOptions", map[string]string{"uid": metadata.UID, "resourceVersion": metadata.ResourceVersion}}
	file, err := os.CreateTemp("", "paw-delete-*.json")
	if err != nil {
		fmt.Fprintf(stderr, "paw: prepare deletion: %v\n", err)
		return 1
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(request)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "paw: could not write deletion preconditions")
		return 1
	}
	if err := deps.runCommand("kubectl", []string{
		"--context", options.context, "--request-timeout=10s",
		"delete", "--raw=/api/v1/namespaces/" + workspaceNamespace, "-f", file.Name(),
	}, io.Discard, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: delete workspace namespace: %v; inspect ownership before retrying\n", err)
		return 1
	}
	if err := deps.runCommand("kubectl", []string{
		"--context", options.context, "--request-timeout=130s",
		"wait", "--for=delete", "namespace/" + workspaceNamespace, "--timeout=120s",
	}, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: workspace deletion has not completed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "workspace namespace deleted; backing-volume reclamation remains storage-adapter dependent")
	return 0
}
