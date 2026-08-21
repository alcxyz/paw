package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"text/tabwriter"

	deployment "git.alc.xyz/alcxyz/paw/deploy"
	"git.alc.xyz/alcxyz/paw/internal/buildinfo"
	"git.alc.xyz/alcxyz/paw/internal/profile"
)

type pathLookup func(string) (string, error)
type commandRunner func(string, []string, io.Writer, io.Writer) error
type manifestMaterializer func(string, deployment.Selection) (string, func(), error)

type dependencies struct {
	lookPath    pathLookup
	runCommand  commandRunner
	materialize manifestMaterializer
}

// Run executes the PAW CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, exec.LookPath)
}

func run(args []string, stdout, stderr io.Writer, lookPath pathLookup) int {
	return runWithDependencies(args, stdout, stderr, dependencies{
		lookPath:    lookPath,
		runCommand:  executeCommand,
		materialize: deployment.MaterializeSelection,
	})
}

func runWithDependencies(args []string, stdout, stderr io.Writer, deps dependencies) int {
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
		return runDoctor(stdout, deps.lookPath)
	case "profile":
		if len(args) == 2 && args[1] == "list" {
			printProfiles(stdout)
			return 0
		}
		if len(args) == 3 && args[1] == "show" {
			return printProfile(args[2], false, stdout, stderr)
		}
		if len(args) == 4 && args[1] == "show" && args[3] == "--json" {
			return printProfile(args[2], true, stdout, stderr)
		}
		return usageError(stderr, "usage: paw profile list | paw profile show NAME [--json]")
	case "workspace":
		return runWorkspace(args[1:], stdout, stderr, deps)
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
  profile       List or inspect built-in workspace profiles
  workspace     Render, create, or destroy a workspace
  version       Print build version information
  help          Show this help`)
}

type workspaceOptions struct {
	adapter     string
	context     string
	deleteState bool
	profile     string
	provider    string
}

func runWorkspace(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 || !slices.Contains([]string{"render", "create", "destroy"}, args[0]) {
		return usageError(stderr, workspaceUsage())
	}
	operation := args[0]
	options, err := parseWorkspaceOptions(args[1:])
	if err != nil {
		return usageError(stderr, err.Error())
	}
	if options.adapter == "" {
		return usageError(stderr, "workspace requires --adapter minikube")
	}
	if operation == "render" && (options.context != "" || options.deleteState) {
		return usageError(stderr, "workspace render does not accept --context or --delete-state")
	}
	if operation != "render" && options.context == "" {
		return usageError(stderr, "workspace create and destroy require --context")
	}
	if operation != "destroy" && options.deleteState {
		return usageError(stderr, "--delete-state is only valid for workspace destroy")
	}
	if operation == "destroy" && !options.deleteState {
		return usageError(stderr, "workspace destroy requires --delete-state for the v0 ephemeral workspace")
	}
	if operation == "destroy" && (options.profile != "" || options.provider != "") {
		return usageError(stderr, "workspace destroy does not accept --profile or --provider")
	}
	if operation != "destroy" && options.profile == "" {
		return usageError(stderr, "workspace render and create require --profile")
	}
	if operation != "destroy" && options.provider == "" {
		return usageError(stderr, "workspace render and create require --provider")
	}

	selection := deployment.Selection{Profile: "core", Provider: "none"}
	if operation != "destroy" {
		selection = deployment.Selection{
			Profile:  options.profile,
			Provider: options.provider,
		}
	}
	if _, err := deployment.DevelopmentImage(selection); err != nil {
		return usageError(stderr, err.Error())
	}
	manifestPath, cleanup, err := deps.materialize(options.adapter, selection)
	if err != nil {
		fmt.Fprintf(stderr, "paw: %v\n", err)
		return 1
	}
	defer cleanup()

	commandArgs := []string{"kustomize", manifestPath}
	if operation == "create" {
		commandArgs = []string{"--context", options.context, "apply", "-k", manifestPath}
	}
	if operation == "destroy" {
		commandArgs = []string{
			"--context", options.context,
			"delete", "-k", manifestPath,
			"--ignore-not-found=true",
		}
	}
	if err := deps.runCommand("kubectl", commandArgs, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: kubectl %s failed: %v\n", operation, err)
		return 1
	}
	return 0
}

func parseWorkspaceOptions(args []string) (workspaceOptions, error) {
	var result workspaceOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--adapter":
			index++
			if index == len(args) || args[index] == "" {
				return workspaceOptions{}, fmt.Errorf("--adapter requires a value")
			}
			if result.adapter != "" {
				return workspaceOptions{}, fmt.Errorf("--adapter may only be specified once")
			}
			result.adapter = args[index]
		case "--context":
			index++
			if index == len(args) || args[index] == "" {
				return workspaceOptions{}, fmt.Errorf("--context requires a value")
			}
			if result.context != "" {
				return workspaceOptions{}, fmt.Errorf("--context may only be specified once")
			}
			result.context = args[index]
		case "--delete-state":
			if result.deleteState {
				return workspaceOptions{}, fmt.Errorf("--delete-state may only be specified once")
			}
			result.deleteState = true
		case "--profile":
			index++
			if index == len(args) || args[index] == "" {
				return workspaceOptions{}, fmt.Errorf("--profile requires a value")
			}
			if result.profile != "" {
				return workspaceOptions{}, fmt.Errorf("--profile may only be specified once")
			}
			result.profile = args[index]
		case "--provider":
			index++
			if index == len(args) || args[index] == "" {
				return workspaceOptions{}, fmt.Errorf("--provider requires a value")
			}
			if result.provider != "" {
				return workspaceOptions{}, fmt.Errorf("--provider may only be specified once")
			}
			result.provider = args[index]
		default:
			return workspaceOptions{}, fmt.Errorf("unknown workspace option %q", args[index])
		}
	}
	return result, nil
}

func workspaceUsage() string {
	return `usage:
  paw workspace render --adapter minikube --profile PROFILE --provider PROVIDER
  paw workspace create --adapter minikube --context CONTEXT --profile PROFILE --provider PROVIDER
  paw workspace destroy --adapter minikube --context CONTEXT --delete-state`
}

func executeCommand(name string, args []string, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
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

func printProfile(name string, asJSON bool, stdout, stderr io.Writer) int {
	definition, exists := profile.Lookup(name)
	if !exists {
		fmt.Fprintf(stderr, "paw: unknown profile %q\n", name)
		return 2
	}

	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(definition); err != nil {
			fmt.Fprintf(stderr, "paw: encode profile: %v\n", err)
			return 1
		}
		return 0
	}

	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "NAME\t%s\n", definition.Name)
	fmt.Fprintf(writer, "AUTHORITY CEILING\t%s\n", definition.Authority)
	fmt.Fprintf(writer, "EXTERNAL MUTATION\t%s\n", allowed(definition.ExternalMutation))
	fmt.Fprintf(writer, "PRODUCTION ACCESS\t%s\n", allowed(definition.ProductionAccess))
	fmt.Fprintf(writer, "PLATFORM IDENTITY\t%s\n", required(definition.PlatformIdentity))
	fmt.Fprintf(writer, "REPOSITORY SELECTION\t%s\n", definition.RepositorySelection)
	fmt.Fprintf(writer, "REMOTE GIT PUSH\t%s\n", allowed(definition.RemoteGitPush))
	fmt.Fprintf(writer, "DEFAULT-DENY EGRESS\t%s\n", enabled(definition.DefaultDenyEgress))
	fmt.Fprintf(writer, "ALLOWED CAPABILITIES\t%s\n", definition.AllowedCapabilities)
	fmt.Fprintf(writer, "EGRESS PURPOSES\t%s\n", definition.EgressPurposes)
	fmt.Fprintf(writer, "FORBIDDEN CAPABILITIES\t%s\n", definition.ForbiddenCapabilities)
	fmt.Fprintf(writer, "REQUIRED ADAPTERS\t%s\n", definition.RequiredAdapters)
	_ = writer.Flush()
	return 0
}

func allowed(value bool) string {
	if value {
		return "allowed"
	}
	return "denied"
}

func required(value bool) string {
	if value {
		return "required"
	}
	return "not required"
}

func enabled(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
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
