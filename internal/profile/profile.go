package profile

// Definition is the user-visible portion of a built-in workspace profile.
// Runtime enforcement belongs to the emitted Kubernetes and identity contracts.
type Definition struct {
	Name        string
	Authority   string
	Description string
}

var builtins = []Definition{
	{
		Name:        "core",
		Authority:   "workspace-only",
		Description: "Headless T3 runtime and essential workspace tools",
	},
	{
		Name:        "platform-readonly",
		Authority:   "read-only",
		Description: "Selected repositories and read-only platform inspection",
	},
}

// Builtins returns a copy so callers cannot mutate the profile catalog.
func Builtins() []Definition {
	profiles := make([]Definition, len(builtins))
	copy(profiles, builtins)
	return profiles
}
