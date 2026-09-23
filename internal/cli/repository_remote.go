package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	deployment "github.com/alcxyz/paw/deploy"
	"github.com/alcxyz/paw/internal/repository"
)

// repositoryRemoteServices are the only Git services the host may run inside
// a workspace repository through the ext:: transport (ADR-018). upload-pack
// serves fetches from the workspace; receive-pack accepts pushes into it.
var repositoryRemoteServices = map[string]bool{
	"git-upload-pack":  true,
	"git-receive-pack": true,
}

// runWorkspaceRepositoryRemote implements the host side of a Git ext:: remote.
// Git invokes it with the service name substituted for %S, connects its
// protocol stream to the command's stdin and stdout, and PAW tunnels that
// stream to the service running against the workspace copy. The workspace
// itself gains no remote, credential, or network access; the host initiates
// every transfer with its own Git identity.
func runWorkspaceRepositoryRemote(args []string, stdout, stderr io.Writer, deps dependencies) int {
	var adapter, contextName, name, service string
	values := map[string]*string{
		"--adapter": &adapter,
		"--context": &contextName,
		"--name":    &name,
		"--service": &service,
	}
	for index := 0; index < len(args); index++ {
		option := args[index]
		target, exists := values[option]
		if !exists {
			return usageError(stderr, fmt.Sprintf("unknown repository option %q", option))
		}
		index++
		if optionValueMissing(args, index) {
			return usageError(stderr, fmt.Sprintf("%s requires a value", option))
		}
		if *target != "" {
			return usageError(stderr, fmt.Sprintf("%s may only be specified once", option))
		}
		*target = args[index]
	}
	if !applyUserDefaults(&adapter, &contextName, true, deps, stderr) {
		return 1
	}
	if adapter == "" || contextName == "" || name == "" || service == "" {
		return usageError(stderr, workspaceRepositoryUsage())
	}
	if !deployment.SupportsAdapter(adapter) {
		return usageError(stderr, fmt.Sprintf("unsupported adapter %q", adapter))
	}
	if !repository.ValidName(name) {
		return usageError(stderr, "repository name must use 1-64 letters, digits, dots, underscores, or hyphens")
	}
	if !repositoryRemoteServices[service] {
		return usageError(stderr, "--service must be git-upload-pack or git-receive-pack")
	}
	if deps.runInput == nil {
		fmt.Fprintln(stderr, "paw: repository remote transport is unavailable")
		return 1
	}
	kubectlArgs := []string{
		"--context", contextName,
		"--namespace", workspaceNamespace,
		"exec", "--stdin", "--quiet", "workspace-0", "--container", "t3", "--",
		service, "/workspace/work/" + name,
	}
	// Git owns both ends of the stream; PAW must not write anything else to
	// stdout. Diagnostics from kubectl or the service go to stderr only.
	if err := deps.runInput(context.Background(), "kubectl", kubectlArgs, os.Stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: repository remote %s did not complete: %v\n", service, err)
		return 1
	}
	return 0
}
