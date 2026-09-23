package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	deployment "github.com/alcxyz/paw/deploy"
)

// userDefaults are the adapter and context PAW uses when a command omits
// --adapter or --context. Explicit flags win, then the PAW_ADAPTER and
// PAW_CONTEXT environment variables, then the user config file written by
// `paw config set`. They never apply where a command refuses the option.
type userDefaults struct {
	Adapter string `json:"adapter,omitempty"`
	Context string `json:"context,omitempty"`
}

const (
	configEnvironmentPath = "PAW_CONFIG"
	configFileName        = "config.json"
)

func environmentLookup(deps dependencies) func(string) string {
	if deps.getenv == nil {
		return func(string) string { return "" }
	}
	return deps.getenv
}

func userConfigPath(deps dependencies) (string, error) {
	getenv := environmentLookup(deps)
	if explicit := strings.TrimSpace(getenv(configEnvironmentPath)); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", errors.New("PAW_CONFIG must be an absolute path")
		}
		return explicit, nil
	}
	if deps.configDir == nil {
		return "", errors.New("no user configuration directory is available")
	}
	base, err := deps.configDir()
	if err != nil || base == "" {
		return "", errors.New("no user configuration directory is available")
	}
	return filepath.Join(base, "paw", configFileName), nil
}

func readUserConfigFile(deps dependencies) (userDefaults, error) {
	if deps.configDir == nil && strings.TrimSpace(environmentLookup(deps)(configEnvironmentPath)) == "" {
		// No configuration source at all: behave as if the file is absent.
		return userDefaults{}, nil
	}
	path, err := userConfigPath(deps)
	if err != nil {
		return userDefaults{}, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return userDefaults{}, nil
	}
	if err != nil {
		return userDefaults{}, fmt.Errorf("read %s: %w", path, err)
	}
	var stored userDefaults
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return userDefaults{}, fmt.Errorf("invalid %s: only \"adapter\" and \"context\" string keys are accepted", path)
	}
	return stored, nil
}

// loadUserDefaults resolves defaults from the environment and the config
// file. A missing file yields empty defaults; an invalid file is an error so
// a typo never silently targets the wrong cluster.
func loadUserDefaults(deps dependencies) (userDefaults, error) {
	stored, err := readUserConfigFile(deps)
	if err != nil {
		return userDefaults{}, err
	}
	getenv := environmentLookup(deps)
	if value := strings.TrimSpace(getenv("PAW_ADAPTER")); value != "" {
		stored.Adapter = value
	}
	if value := strings.TrimSpace(getenv("PAW_CONTEXT")); value != "" {
		stored.Context = value
	}
	return stored, nil
}

// applyUserDefaults fills empty adapter and context values from the
// defaults. Context is only filled when the command accepts one.
func applyUserDefaults(adapter, contextName *string, acceptsContext bool, deps dependencies, stderr io.Writer) bool {
	if *adapter != "" && (*contextName != "" || !acceptsContext) {
		return true
	}
	defaults, err := loadUserDefaults(deps)
	if err != nil {
		fmt.Fprintf(stderr, "paw: %v\n", err)
		return false
	}
	if *adapter == "" {
		*adapter = defaults.Adapter
	}
	if acceptsContext && *contextName == "" {
		*contextName = defaults.Context
	}
	return true
}

// applyDefaultContext fills an empty context from the defaults for commands
// that take no adapter.
func applyDefaultContext(contextName *string, deps dependencies, stderr io.Writer) bool {
	if *contextName != "" {
		return true
	}
	defaults, err := loadUserDefaults(deps)
	if err != nil {
		fmt.Fprintf(stderr, "paw: %v\n", err)
		return false
	}
	*contextName = defaults.Context
	return true
}

func writeUserConfigFile(deps dependencies, stored userDefaults) error {
	path, err := userConfigPath(deps)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o600)
}

func configUsage() string {
	return `usage:
  paw config show
  paw config set adapter ADAPTER | paw config set context CONTEXT
  paw config unset adapter | paw config unset context

Defaults fill --adapter and --context when a command omits them. Precedence:
explicit flag, then PAW_ADAPTER / PAW_CONTEXT, then this file. The file lives
at $PAW_CONFIG or the user config directory under paw/config.json.`
}

func runConfig(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, configUsage())
	}
	switch args[0] {
	case "show":
		if len(args) != 1 {
			return usageError(stderr, configUsage())
		}
		path, err := userConfigPath(deps)
		if err != nil {
			fmt.Fprintf(stderr, "paw: %v\n", err)
			return 1
		}
		stored, err := readUserConfigFile(deps)
		if err != nil {
			fmt.Fprintf(stderr, "paw: %v\n", err)
			return 1
		}
		effective, err := loadUserDefaults(deps)
		if err != nil {
			fmt.Fprintf(stderr, "paw: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "FILE       %s\nADAPTER    %s\nCONTEXT    %s\n", path, describeDefault(stored.Adapter, effective.Adapter), describeDefault(stored.Context, effective.Context))
		return 0
	case "set":
		if len(args) != 3 {
			return usageError(stderr, configUsage())
		}
		key, value := args[1], strings.TrimSpace(args[2])
		if value == "" || strings.ContainsAny(value, " \t\r\n\"'") {
			return usageError(stderr, "config values must be single words without quotes")
		}
		stored, err := readUserConfigFile(deps)
		if err != nil {
			fmt.Fprintf(stderr, "paw: %v\n", err)
			return 1
		}
		switch key {
		case "adapter":
			if !deployment.SupportsAdapter(value) {
				return usageError(stderr, fmt.Sprintf("unsupported adapter %q", value))
			}
			stored.Adapter = value
		case "context":
			stored.Context = value
		default:
			return usageError(stderr, configUsage())
		}
		if err := writeUserConfigFile(deps, stored); err != nil {
			fmt.Fprintf(stderr, "paw: could not write the config file: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "default %s set to %s\n", key, value)
		return 0
	case "unset":
		if len(args) != 2 {
			return usageError(stderr, configUsage())
		}
		stored, err := readUserConfigFile(deps)
		if err != nil {
			fmt.Fprintf(stderr, "paw: %v\n", err)
			return 1
		}
		switch args[1] {
		case "adapter":
			stored.Adapter = ""
		case "context":
			stored.Context = ""
		default:
			return usageError(stderr, configUsage())
		}
		if err := writeUserConfigFile(deps, stored); err != nil {
			fmt.Fprintf(stderr, "paw: could not write the config file: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "default %s cleared\n", args[1])
		return 0
	default:
		return usageError(stderr, configUsage())
	}
}

func describeDefault(stored, effective string) string {
	switch {
	case effective == "":
		return "(unset)"
	case stored == effective:
		return effective
	default:
		return effective + " (from environment)"
	}
}
