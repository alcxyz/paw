package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	deployment "github.com/alcxyz/paw/deploy"
	"github.com/alcxyz/paw/internal/repository"
)

// repositoryRemoteURL is the Git ext:: URL that reaches a workspace repository
// through `paw workspace repository remote` (ADR-018). It names the paw
// command itself rather than an absolute path so it keeps working across
// rebuilds of the host that installs paw.
func repositoryRemoteURL(command, adapter, contextName, name string) string {
	return fmt.Sprintf(
		"ext::%s workspace repository remote --adapter %s --context %s --name %s --service %%S",
		command, adapter, contextName, name,
	)
}

// runWorkspaceRepositoryLink adds the workspace repository as a Git remote of
// the current checkout so the operator can fetch and push with plain Git.
func runWorkspaceRepositoryLink(args []string, stdout, stderr io.Writer, deps dependencies) int {
	var adapter, contextName, name, remote, command string
	values := map[string]*string{
		"--adapter": &adapter,
		"--context": &contextName,
		"--name":    &name,
		"--remote":  &remote,
		"--command": &command,
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
	if adapter == "" || contextName == "" || name == "" {
		return usageError(stderr, workspaceRepositoryUsage())
	}
	if !deployment.SupportsAdapter(adapter) {
		return usageError(stderr, fmt.Sprintf("unsupported adapter %q", adapter))
	}
	if !repository.ValidName(name) {
		return usageError(stderr, "repository name must use 1-64 letters, digits, dots, underscores, or hyphens")
	}
	if strings.ContainsAny(contextName, " \t\r\n\"'") {
		return usageError(stderr, "--context must not contain whitespace or quotes")
	}
	if remote == "" {
		remote = "paw"
	}
	if !repository.ValidName(remote) {
		return usageError(stderr, "--remote must use 1-64 letters, digits, dots, underscores, or hyphens")
	}
	if command == "" {
		command = "paw"
	}
	if strings.ContainsAny(command, " \t\r\n\"'") {
		return usageError(stderr, "--command must not contain whitespace or quotes")
	}

	if err := deps.runCommand("git", []string{"rev-parse", "--is-inside-work-tree"}, io.Discard, io.Discard); err != nil {
		fmt.Fprintln(stderr, "paw: repository link must run inside the Git checkout to link")
		return 1
	}
	var allow bytes.Buffer
	_ = deps.runCommand("git", []string{"config", "--get", "protocol.ext.allow"}, &allow, io.Discard)
	switch strings.TrimSpace(allow.String()) {
	case "user", "always":
	default:
		fmt.Fprintln(stderr, "paw: Git disables ext:: transports by default; allow them once with: git config --global protocol.ext.allow user")
		return 1
	}
	url := repositoryRemoteURL(command, adapter, contextName, name)
	if err := deps.runCommand("git", []string{"remote", "add", remote, url}, io.Discard, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: could not add remote %q; if it exists, update it with: git remote set-url %s %q\n", remote, remote, url)
		return 1
	}
	fmt.Fprintf(stdout, "remote %s linked to workspace repository %s; use git fetch %s and git push %s BRANCH:refs/heads/NAME\n", remote, name, remote, remote)
	return 0
}
