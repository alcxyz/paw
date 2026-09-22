package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	deployment "github.com/alcxyz/paw/deploy"
	"github.com/alcxyz/paw/internal/buildinfo"
	"github.com/alcxyz/paw/internal/environment"
	"github.com/alcxyz/paw/internal/profile"
	"github.com/alcxyz/paw/internal/repository"
)

type pathLookup func(string) (string, error)
type commandRunner func(string, []string, io.Writer, io.Writer) error
type inputCommandRunner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
type manifestMaterializer func(deployment.ManifestRequest) (string, func(), error)
type repositoryAdder func(repository.Request, io.Writer, io.Writer) error
type repositoryExporter func(context.Context, repository.ExportRequest) error
type environmentVerifier func(context.Context, environment.Request) (environment.Report, error)

type dependencies struct {
	lookPath       pathLookup
	runCommand     commandRunner
	runInput       inputCommandRunner
	runInteractive interactiveRunner
	materialize    manifestMaterializer
	addRepo        repositoryAdder
	exportRepo     repositoryExporter
	verifyEnv      environmentVerifier
}

// Run executes the PAW CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, exec.LookPath)
}

func run(args []string, stdout, stderr io.Writer, lookPath pathLookup) int {
	return runWithDependencies(args, stdout, stderr, dependencies{
		lookPath:       lookPath,
		runCommand:     executeCommand,
		runInput:       executeCommandWithInput,
		runInteractive: executeInteractive,
		materialize:    deployment.MaterializeManifest,
		addRepo:        repository.Add,
		exportRepo:     repository.Export,
		verifyEnv:      environment.Verify,
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
	case "environment":
		return runEnvironment(args[1:], stdout, stderr, deps)
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
  environment   Verify a selected runtime environment
  profile       List or inspect built-in workspace profiles
  workspace     Render and operate a workspace
  version       Print build version information
  help          Show this help`)
}

func runEnvironment(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 || args[0] != "verify" {
		return usageError(stderr, environmentUsage())
	}

	var contextName string
	jsonOutput := false
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--context":
			index++
			if optionValueMissing(args, index) {
				return usageError(stderr, "--context requires a value")
			}
			if contextName != "" {
				return usageError(stderr, "--context may only be specified once")
			}
			contextName = args[index]
		case "--json":
			if jsonOutput {
				return usageError(stderr, "--json may only be specified once")
			}
			jsonOutput = true
		default:
			return usageError(stderr, fmt.Sprintf("unknown environment option %q", args[index]))
		}
	}
	if contextName == "" {
		return usageError(stderr, "environment verify requires --context")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, verifyErr := deps.verifyEnv(ctx, environment.Request{Context: contextName})
	var outputErr error
	if jsonOutput {
		outputErr = environment.WriteJSON(stdout, report)
	} else {
		outputErr = environment.WriteText(stdout, report)
	}
	if outputErr != nil {
		fmt.Fprintf(stderr, "paw: write environment verification: %v\n", outputErr)
		return 1
	}
	if verifyErr != nil {
		fmt.Fprintf(stderr, "paw: environment verification: %v\n", verifyErr)
		return 1
	}
	if report.Outcome != environment.OutcomePass {
		return 1
	}
	return 0
}

func environmentUsage() string {
	return "usage: paw environment verify --context CONTEXT [--json]"
}

type workspaceOptions struct {
	adapter             string
	backupInput         string
	backupOutput        string
	confirmEmptyRestore bool
	context             string
	deleteState         bool
	imageRef            string
	egressImageRef      string
	helperImageRef      string
	jsonOutput          bool
	label               string
	localPort           int
	pairingID           string
	profile             string
	provider            string
	ttl                 string
}

func runWorkspace(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) > 0 && args[0] == "repository" {
		return runWorkspaceRepository(args[1:], stdout, stderr, deps)
	}
	if len(args) == 0 || !slices.Contains(
		[]string{"render", "create", "inspect", "connect", "pair", "revoke", "upgrade-check", "backup", "restore", "login", "logout", "destroy"},
		args[0],
	) {
		return usageError(stderr, workspaceUsage())
	}
	operation := args[0]
	options, err := parseWorkspaceOptions(args[1:])
	if err != nil {
		return usageError(stderr, err.Error())
	}
	if options.adapter == "" {
		return usageError(stderr, "workspace requires --adapter kubernetes or minikube")
	}
	if !deployment.SupportsAdapter(options.adapter) {
		return usageError(stderr, fmt.Sprintf("unsupported adapter %q", options.adapter))
	}
	if operation != "render" && options.context == "" {
		return usageError(stderr, fmt.Sprintf("workspace %s requires --context", operation))
	}
	if operation == "render" && options.context != "" {
		return usageError(stderr, "workspace render does not accept --context")
	}
	if operation != "destroy" && options.deleteState {
		return usageError(stderr, "--delete-state is only valid for workspace destroy")
	}
	if operation == "destroy" && !options.deleteState {
		return usageError(stderr, "workspace destroy requires --delete-state to delete the retained workspace claims")
	}
	if slices.Contains([]string{"render", "create"}, operation) && options.profile == "" {
		return usageError(stderr, "workspace render and create require --profile")
	}
	if slices.Contains([]string{"render", "create"}, operation) && options.provider == "" {
		return usageError(stderr, "workspace render and create require --provider")
	}
	if slices.Contains([]string{"login", "logout"}, operation) && options.provider == "" {
		return usageError(stderr, "workspace login and logout require --provider")
	}
	if !slices.Contains([]string{"render", "create"}, operation) && options.profile != "" {
		return usageError(stderr, fmt.Sprintf("workspace %s does not accept --profile", operation))
	}
	if !slices.Contains([]string{"render", "create", "login", "logout"}, operation) && options.provider != "" {
		return usageError(stderr, fmt.Sprintf("workspace %s does not accept --provider", operation))
	}
	if operation != "pair" && (options.ttl != "" || options.label != "") {
		return usageError(stderr, "--ttl and --label are only valid for workspace pair")
	}
	if operation != "revoke" && options.pairingID != "" {
		return usageError(stderr, "--pairing-id is only valid for workspace revoke")
	}
	if operation == "revoke" && options.pairingID == "" {
		return usageError(stderr, "workspace revoke requires --pairing-id")
	}
	if !slices.Contains([]string{"connect", "pair"}, operation) && options.localPort != 0 {
		return usageError(stderr, "--local-port is only valid for workspace connect and pair")
	}
	if !slices.Contains([]string{"inspect", "pair"}, operation) && options.jsonOutput {
		return usageError(stderr, "--json is only valid for workspace inspect and pair")
	}
	if !slices.Contains([]string{"render", "create"}, operation) && options.imageRef != "" {
		return usageError(stderr, "--image-ref is only valid for workspace render and create")
	}
	if !slices.Contains([]string{"render", "create"}, operation) && options.egressImageRef != "" {
		return usageError(stderr, "--egress-image-ref is only valid for workspace render and create")
	}
	if operation != "backup" && options.backupOutput != "" {
		return usageError(stderr, "--output is only valid for workspace backup")
	}
	if operation != "restore" && options.backupInput != "" {
		return usageError(stderr, "--input is only valid for workspace restore")
	}
	if operation != "restore" && options.confirmEmptyRestore {
		return usageError(stderr, "--confirm-empty-restore is only valid for workspace restore")
	}
	if !slices.Contains([]string{"backup", "restore"}, operation) && options.helperImageRef != "" {
		return usageError(stderr, "--helper-image-ref is only valid for workspace backup and restore")
	}
	if operation == "backup" && options.backupOutput == "" {
		return usageError(stderr, "workspace backup requires --output")
	}
	if operation == "restore" && (options.backupInput == "" || !options.confirmEmptyRestore) {
		return usageError(stderr, "workspace restore requires --input and --confirm-empty-restore")
	}
	if slices.Contains([]string{"backup", "restore"}, operation) {
		if options.adapter == deployment.AdapterKubernetes && options.helperImageRef == "" {
			return usageError(stderr, "adapter kubernetes requires --helper-image-ref for workspace backup and restore")
		}
		if options.adapter == deployment.AdapterMinikube && options.helperImageRef != "" {
			return usageError(stderr, "adapter minikube does not accept --helper-image-ref")
		}
		if options.helperImageRef != "" {
			if err := validateBackupHelperImage(options.helperImageRef); err != nil {
				return usageError(stderr, err.Error())
			}
		}
	}

	switch operation {
	case "inspect":
		return runWorkspaceInspect(options, stdout, stderr, deps)
	case "upgrade-check":
		return runWorkspaceUpgradeCheck(options, stdout, stderr, deps)
	case "backup":
		return runWorkspaceBackup(options, stdout, stderr, deps)
	case "restore":
		return runWorkspaceRestore(options, stdout, stderr, deps)
	case "connect":
		return runWorkspaceConnect(options, stdout, stderr, deps)
	case "pair":
		return runWorkspacePair(options, stdout, stderr, deps)
	case "revoke":
		return runWorkspaceRevoke(options, stdout, stderr, deps)
	case "destroy":
		return runWorkspaceDestroy(options, stdout, stderr, deps)
	case "login", "logout":
		return runWorkspaceSession(operation, options, stdout, stderr, deps)
	}

	selection := deployment.Selection{
		Profile:  options.profile,
		Provider: options.provider,
	}
	if _, err := deployment.ImageName(selection); err != nil {
		return usageError(stderr, err.Error())
	}
	if options.adapter == deployment.AdapterKubernetes &&
		slices.Contains([]string{"render", "create"}, operation) && options.imageRef == "" {
		return usageError(stderr, "adapter kubernetes requires --image-ref for workspace render and create")
	}
	if options.adapter == deployment.AdapterKubernetes &&
		slices.Contains([]string{"render", "create"}, operation) && options.egressImageRef == "" {
		return usageError(stderr, "adapter kubernetes requires --egress-image-ref for workspace render and create")
	}
	if options.adapter == deployment.AdapterMinikube && options.imageRef != "" {
		return usageError(stderr, "adapter minikube does not accept --image-ref")
	}
	if options.adapter == deployment.AdapterMinikube && options.egressImageRef != "" {
		return usageError(stderr, "adapter minikube does not accept --egress-image-ref")
	}
	if options.imageRef != "" {
		if _, _, err := deployment.ValidateReleasedImageReference(selection, options.imageRef); err != nil {
			return usageError(stderr, err.Error())
		}
	}
	if options.egressImageRef != "" {
		if _, _, err := deployment.ValidateEgressImageReference(options.egressImageRef); err != nil {
			return usageError(stderr, err.Error())
		}
	}
	manifestPath, cleanup, err := deps.materialize(deployment.ManifestRequest{
		Adapter:              options.adapter,
		Selection:            selection,
		ImageReference:       options.imageRef,
		EgressImageReference: options.egressImageRef,
	})
	if err != nil {
		fmt.Fprintf(stderr, "paw: %v\n", err)
		return 1
	}
	defer cleanup()

	if operation == "create" {
		return runWorkspaceCreate(options, manifestPath, stdout, stderr, deps)
	}
	commandArgs := []string{"kustomize", manifestPath}
	if err := deps.runCommand("kubectl", commandArgs, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: kubectl %s failed: %v\n", operation, err)
		return 1
	}
	return 0
}

func runWorkspaceRepository(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, workspaceRepositoryUsage())
	}
	switch args[0] {
	case "add":
		return runWorkspaceRepositoryAdd(args[1:], stdout, stderr, deps)
	case "export":
		return runWorkspaceRepositoryExport(args[1:], stdout, stderr, deps)
	default:
		return usageError(stderr, workspaceRepositoryUsage())
	}
}

func runWorkspaceRepositoryAdd(args []string, stdout, stderr io.Writer, deps dependencies) int {
	var adapter string
	var context string
	var name string
	var revision string
	var source string
	values := map[string]*string{
		"--adapter":  &adapter,
		"--context":  &context,
		"--name":     &name,
		"--revision": &revision,
		"--source":   &source,
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

	if adapter == "" || context == "" || name == "" || revision == "" || source == "" {
		return usageError(stderr, workspaceRepositoryUsage())
	}
	if !deployment.SupportsAdapter(adapter) {
		return usageError(stderr, fmt.Sprintf("unsupported adapter %q", adapter))
	}
	request := repository.Request{
		Context:  context,
		Name:     name,
		Revision: revision,
		Source:   source,
	}
	if err := deps.addRepo(request, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "paw: materialize repository: %v\n", err)
		return 1
	}
	return 0
}

func runWorkspaceRepositoryExport(args []string, stdout, stderr io.Writer, deps dependencies) int {
	var adapter string
	var baseCommit string
	var contextName string
	var name string
	var output string
	values := map[string]*string{
		"--adapter":     &adapter,
		"--base-commit": &baseCommit,
		"--context":     &contextName,
		"--name":        &name,
		"--output":      &output,
	}
	for index := 0; index < len(args); index++ {
		option := args[index]
		target, exists := values[option]
		if !exists {
			return usageError(stderr, fmt.Sprintf("unknown repository export option %q", option))
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

	if adapter == "" || baseCommit == "" || contextName == "" || name == "" || output == "" {
		return usageError(stderr, workspaceRepositoryUsage())
	}
	if !deployment.SupportsAdapter(adapter) {
		return usageError(stderr, fmt.Sprintf("unsupported adapter %q", adapter))
	}
	request := repository.ExportRequest{
		BaseCommit: baseCommit,
		Context:    contextName,
		Name:       name,
		Output:     output,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := deps.exportRepo(ctx, request); err != nil {
		fmt.Fprintf(stderr, "paw: export repository: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "repository %s exported from %s to %s\n", name, baseCommit, output)
	return 0
}

func runWorkspaceInspect(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	output := "wide"
	if options.jsonOutput {
		output = "json"
	}
	args := []string{
		"--context", options.context,
		"--namespace", "paw-workspace",
		"get",
		"statefulset/workspace",
		"pod/workspace-0",
		"persistentvolumeclaim/workspace-state",
		"service/t3",
		"deployment/egress",
		"--output", output,
	}
	return runKubectl("inspect", args, stdout, stderr, deps)
}

func runWorkspaceConnect(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	localPort := selectedLocalPort(options.localPort)
	args := []string{
		"--context", options.context,
		"--namespace", "paw-workspace",
		"port-forward",
		"--address", "127.0.0.1",
		"service/t3",
		fmt.Sprintf("%d:3773", localPort),
	}
	return runKubectl("connect", args, stdout, stderr, deps)
}

func runWorkspacePair(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	localPort := selectedLocalPort(options.localPort)
	ttl := options.ttl
	if ttl == "" {
		ttl = "10m"
	}
	duration, err := time.ParseDuration(ttl)
	if err != nil || duration <= 0 || duration > time.Hour {
		return usageError(stderr, "--ttl must be a positive Go duration no longer than 1h")
	}
	if len(options.label) > 80 {
		return usageError(stderr, "--label must not exceed 80 bytes")
	}

	args := []string{
		"--context", options.context,
		"--namespace", "paw-workspace",
		"exec", "workspace-0", "--",
		"t3", "auth", "pairing", "create",
		"--base-dir", "/workspace/state/t3",
		"--base-url", fmt.Sprintf("http://127.0.0.1:%d", localPort),
		"--ttl", ttl,
	}
	if options.label != "" {
		args = append(args, "--label", options.label)
	}
	if options.jsonOutput {
		args = append(args, "--json")
	}
	return runKubectl("pair", args, stdout, stderr, deps)
}

func runWorkspaceRevoke(options workspaceOptions, stdout, stderr io.Writer, deps dependencies) int {
	if len(options.pairingID) > 200 || options.pairingID[0] == '-' {
		return usageError(stderr, "--pairing-id must be an identifier of at most 200 bytes")
	}
	args := []string{
		"--context", options.context,
		"--namespace", "paw-workspace",
		"exec", "workspace-0", "--",
		"t3", "auth", "pairing", "revoke",
		"--base-dir", "/workspace/state/t3",
		options.pairingID,
	}
	return runKubectl("revoke", args, stdout, stderr, deps)
}

func selectedLocalPort(port int) int {
	if port == 0 {
		return 3773
	}
	return port
}

func runKubectl(operation string, args []string, stdout, stderr io.Writer, deps dependencies) int {
	if err := deps.runCommand("kubectl", args, stdout, stderr); err != nil {
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
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--adapter requires a value")
			}
			if result.adapter != "" {
				return workspaceOptions{}, fmt.Errorf("--adapter may only be specified once")
			}
			result.adapter = args[index]
		case "--context":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--context requires a value")
			}
			if result.context != "" {
				return workspaceOptions{}, fmt.Errorf("--context may only be specified once")
			}
			result.context = args[index]
		case "--output":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--output requires a value")
			}
			if result.backupOutput != "" {
				return workspaceOptions{}, fmt.Errorf("--output may only be specified once")
			}
			result.backupOutput = args[index]
		case "--input":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--input requires a value")
			}
			if result.backupInput != "" {
				return workspaceOptions{}, fmt.Errorf("--input may only be specified once")
			}
			result.backupInput = args[index]
		case "--helper-image-ref":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--helper-image-ref requires a value")
			}
			if result.helperImageRef != "" {
				return workspaceOptions{}, fmt.Errorf("--helper-image-ref may only be specified once")
			}
			result.helperImageRef = args[index]
		case "--confirm-empty-restore":
			if result.confirmEmptyRestore {
				return workspaceOptions{}, fmt.Errorf("--confirm-empty-restore may only be specified once")
			}
			result.confirmEmptyRestore = true
		case "--image-ref":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--image-ref requires a value")
			}
			if result.imageRef != "" {
				return workspaceOptions{}, fmt.Errorf("--image-ref may only be specified once")
			}
			result.imageRef = args[index]
		case "--egress-image-ref":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--egress-image-ref requires a value")
			}
			if result.egressImageRef != "" {
				return workspaceOptions{}, fmt.Errorf("--egress-image-ref may only be specified once")
			}
			result.egressImageRef = args[index]
		case "--delete-state":
			if result.deleteState {
				return workspaceOptions{}, fmt.Errorf("--delete-state may only be specified once")
			}
			result.deleteState = true
		case "--json":
			if result.jsonOutput {
				return workspaceOptions{}, fmt.Errorf("--json may only be specified once")
			}
			result.jsonOutput = true
		case "--label":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--label requires a value")
			}
			if result.label != "" {
				return workspaceOptions{}, fmt.Errorf("--label may only be specified once")
			}
			result.label = args[index]
		case "--local-port":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--local-port requires a value")
			}
			if result.localPort != 0 {
				return workspaceOptions{}, fmt.Errorf("--local-port may only be specified once")
			}
			port, parseErr := strconv.Atoi(args[index])
			if parseErr != nil || port < 1024 || port > 65535 {
				return workspaceOptions{}, fmt.Errorf("--local-port must be between 1024 and 65535")
			}
			result.localPort = port
		case "--pairing-id":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--pairing-id requires a value")
			}
			if result.pairingID != "" {
				return workspaceOptions{}, fmt.Errorf("--pairing-id may only be specified once")
			}
			result.pairingID = args[index]
		case "--profile":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--profile requires a value")
			}
			if result.profile != "" {
				return workspaceOptions{}, fmt.Errorf("--profile may only be specified once")
			}
			result.profile = args[index]
		case "--provider":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--provider requires a value")
			}
			if result.provider != "" {
				return workspaceOptions{}, fmt.Errorf("--provider may only be specified once")
			}
			result.provider = args[index]
		case "--ttl":
			index++
			if optionValueMissing(args, index) {
				return workspaceOptions{}, fmt.Errorf("--ttl requires a value")
			}
			if result.ttl != "" {
				return workspaceOptions{}, fmt.Errorf("--ttl may only be specified once")
			}
			result.ttl = args[index]
		default:
			return workspaceOptions{}, fmt.Errorf("unknown workspace option %q", args[index])
		}
	}
	return result, nil
}

func optionValueMissing(args []string, index int) bool {
	return index == len(args) || args[index] == "" || strings.HasPrefix(args[index], "--")
}

func workspaceUsage() string {
	return `usage:
  paw workspace render --adapter ADAPTER --profile PROFILE --provider PROVIDER [--image-ref IMAGE@DIGEST --egress-image-ref IMAGE@DIGEST]
  paw workspace create --adapter ADAPTER --context CONTEXT --profile PROFILE --provider PROVIDER [--image-ref IMAGE@DIGEST --egress-image-ref IMAGE@DIGEST]
  paw workspace inspect --adapter ADAPTER --context CONTEXT [--json]
  paw workspace upgrade-check --adapter ADAPTER --context CONTEXT
  paw workspace backup --adapter ADAPTER --context CONTEXT --output ABSOLUTE_NEW_DIRECTORY [--helper-image-ref IMAGE@DIGEST]
  paw workspace restore --adapter ADAPTER --context CONTEXT --input ABSOLUTE_BACKUP_DIRECTORY --confirm-empty-restore [--helper-image-ref IMAGE@DIGEST]
  paw workspace login --adapter ADAPTER --context CONTEXT --provider PROVIDER
  paw workspace logout --adapter ADAPTER --context CONTEXT --provider PROVIDER
  paw workspace connect --adapter ADAPTER --context CONTEXT [--local-port PORT]
  paw workspace pair --adapter ADAPTER --context CONTEXT [--local-port PORT] [--ttl TTL] [--label LABEL] [--json]
  paw workspace revoke --adapter ADAPTER --context CONTEXT --pairing-id ID
  paw workspace repository add --adapter ADAPTER --context CONTEXT --source PATH --revision REF --name NAME
  paw workspace repository export --adapter ADAPTER --context CONTEXT --name NAME --base-commit FULL_SHA --output ABSOLUTE_NEW_PATCH
  paw workspace destroy --adapter ADAPTER --context CONTEXT --delete-state

ADAPTER is kubernetes or minikube. The kubernetes adapter requires immutable
--image-ref and --egress-image-ref values for render and create; minikube uses
the reviewed local :dev images.`
}

func workspaceRepositoryUsage() string {
	return `usage:
  paw workspace repository add --adapter ADAPTER --context CONTEXT --source PATH --revision REF --name NAME
  paw workspace repository export --adapter ADAPTER --context CONTEXT --name NAME --base-commit FULL_SHA --output ABSOLUTE_NEW_PATCH`
}

// interactiveRunner attaches the operator's terminal to a command, for
// provider logins that print a link and read a code (ADR-014).
type interactiveRunner func(name string, args []string) error

func executeInteractive(name string, args []string) error {
	command := exec.Command(name, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func executeCommand(name string, args []string, stdout, stderr io.Writer) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	return executeCommandWithSignals(name, args, stdout, stderr, signals)
}

func executeCommandWithInput(
	ctx context.Context,
	name string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 5 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	err := command.Run()
	if ctx.Err() != nil && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	return err
}

func executeCommandWithSignals(
	name string,
	args []string,
	stdout, stderr io.Writer,
	signals <-chan os.Signal,
) error {
	return executeCommandWithGrace(name, args, stdout, stderr, signals, 5*time.Second)
}

func executeCommandWithGrace(
	name string,
	args []string,
	stdout, stderr io.Writer,
	signals <-chan os.Signal,
	grace time.Duration,
) error {
	command := exec.Command(name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	// Keep the child and its helpers in a separate group so terminal signals
	// arrive once, through PAW, and forced termination includes descendants.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = grace
	if err := command.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()

	var timer *time.Timer
	var deadline <-chan time.Time
	interrupted := false
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case err := <-done:
			if interrupted {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				if err == nil {
					return fmt.Errorf("command interrupted")
				}
			}
			return err
		case received, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			if received == nil {
				continue
			}
			if interrupted {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				continue
			}
			interrupted = true
			if sig, ok := received.(syscall.Signal); ok {
				_ = syscall.Kill(-command.Process.Pid, sig)
			}
			timer = time.NewTimer(grace)
			deadline = timer.C
		case <-deadline:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			deadline = nil
		}
	}
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
		{name: "kubectl", required: true, purpose: "Kubernetes workspace lifecycle"},
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
