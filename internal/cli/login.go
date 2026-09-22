package cli

import (
	"fmt"
	"io"
)

// providerSessionCommands are the reviewed in-pod login and logout commands
// for each provider (ADR-014). The provider tool writes its credential to the
// pod-scoped session volume; PAW never handles the credential itself.
var providerSessionCommands = map[string]struct {
	login  []string
	logout []string
}{
	"codex": {
		login:  []string{"codex", "login", "--device-auth"},
		logout: []string{"codex", "logout"},
	},
	"claude-code": {
		login:  []string{"claude", "auth", "login"},
		logout: []string{"claude", "auth", "logout"},
	},
}

func runWorkspaceSession(operation string, options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	commands, reviewed := providerSessionCommands[options.provider]
	if !reviewed {
		return usageError(stderr, fmt.Sprintf("provider %q has no reviewed login flow", options.provider))
	}
	var statefulSet statefulSetResource
	if err := getUpgradeResource(options.context, workspaceNamespace, []string{"statefulset/workspace"}, &statefulSet, deps); err != nil {
		fmt.Fprintf(stderr, "paw: workspace %s: could not read the workspace: %v\n", operation, err)
		return 1
	}
	recorded := statefulSet.Metadata.Annotations["paw.alc.xyz/provider"]
	if recorded != options.provider {
		fmt.Fprintf(stderr, "paw: workspace %s: the workspace records provider %q, not %q\n", operation, recorded, options.provider)
		return 1
	}
	command := commands.login
	if operation == "logout" {
		command = commands.logout
	}
	if deps.runInteractive == nil {
		fmt.Fprintf(stderr, "paw: workspace %s requires an interactive terminal\n", operation)
		return 1
	}
	args := append([]string{
		"--context", options.context,
		"--namespace", workspaceNamespace,
		"exec", "--stdin", "--tty", "workspace-0", "--container", "t3", "--",
	}, command...)
	if err := deps.runInteractive("kubectl", args); err != nil {
		fmt.Fprintf(stderr, "paw: workspace %s did not complete: %v\n", operation, err)
		return 1
	}
	if operation == "login" {
		fmt.Fprintln(stdout, "workspace login completed; the credential lives on the pod's session volume and is discarded when the pod is replaced")
	} else {
		fmt.Fprintln(stdout, "workspace logout completed")
	}
	return 0
}
