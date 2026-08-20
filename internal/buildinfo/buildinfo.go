package buildinfo

import "fmt"

// These values are replaced through Go linker flags in release builds.
var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}

	return fmt.Sprintf("%s (%s)", Version, Commit)
}
