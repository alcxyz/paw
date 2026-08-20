package cli

import (
	"fmt"
	"io"
	"os/exec"
	"text/tabwriter"

	"git.alc.xyz/alcxyz/paw/internal/buildinfo"
	"git.alc.xyz/alcxyz/paw/internal/profile"
)

type pathLookup func(string) (string, error)

// Run executes the PAW CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, exec.LookPath)
}

func run(args []string, stdout, stderr io.Writer, lookPath pathLookup) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version":
		if len(args) != 1 {
			return usageError(stderr, "version does not accept arguments")
		}
		fmt.Fprintf(stdout, "paw %s\n", buildinfo.String())
		return 0
	case "doctor":
		if len(args) != 1 {
			return usageError(stderr, "doctor does not accept arguments")
		}
		return runDoctor(stdout, lookPath)
	case "profile":
		if len(args) == 2 && args[1] == "list" {
			printProfiles(stdout)
			return 0
		}
		return usageError(stderr, "usage: paw profile list")
	default:
		return usageError(stderr, fmt.Sprintf("unknown command %q", args[0]))
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `PAW — portable AI workspaces

Usage:
  paw <command>

Commands:
  doctor        Inspect local PAW dependencies
  profile list  List built-in workspace profiles
  version       Print build version information
  help          Show this help`)
}

func usageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "paw: %s\n", message)
	return 2
}

func printProfiles(output io.Writer) {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tAUTHORITY\tDESCRIPTION")
	for _, item := range profile.Builtins() {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", item.Name, item.Authority, item.Description)
	}
	_ = writer.Flush()
}

func runDoctor(output io.Writer, lookPath pathLookup) int {
	type dependency struct {
		name     string
		required bool
		purpose  string
	}

	dependencies := []dependency{
		{name: "git", required: true, purpose: "repository operations"},
		{name: "kubectl", purpose: "external Kubernetes diagnostics"},
		{name: "minikube", purpose: "local reference adapter"},
		{name: "nix", purpose: "contributor builds only"},
	}

	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "DEPENDENCY\tSTATUS\tREQUIRED\tPURPOSE")
	missingRequired := false
	for _, item := range dependencies {
		status := "available"
		if _, err := lookPath(item.name); err != nil {
			status = "missing"
			missingRequired = missingRequired || item.required
		}

		required := "no"
		if item.required {
			required = "yes"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.name, status, required, item.purpose)
	}
	_ = writer.Flush()

	if missingRequired {
		return 1
	}
	return 0
}
