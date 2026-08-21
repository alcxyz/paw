// Package contract exposes PAW's versioned, machine-readable capability contract.
package contract

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

//go:embed v0.json
var v0JSON []byte

// AuthorityClass is an ordered ceiling on effective workspace authority.
type AuthorityClass struct {
	Name             string `json:"name"`
	Rank             int    `json:"rank"`
	ExternalMutation bool   `json:"externalMutation"`
	ProductionAccess bool   `json:"productionAccess"`
	Description      string `json:"description"`
}

// Profile defines the security-relevant portion of a released workspace profile.
type Profile struct {
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	AuthorityCeiling      string   `json:"authorityCeiling"`
	RepositorySelection   string   `json:"repositorySelection"`
	RemoteGitPush         bool     `json:"remoteGitPush"`
	DefaultDenyEgress     bool     `json:"defaultDenyEgress"`
	AllowedCapabilities   []string `json:"allowedCapabilities"`
	EgressPurposes        []string `json:"egressPurposes"`
	ForbiddenCapabilities []string `json:"forbiddenCapabilities"`
}

// TrustBoundary documents a principal or system boundary relevant to v0.
type TrustBoundary struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Boundary string `json:"boundary"`
}

// Adapter defines the minimum behavior an environment integration must supply.
type Adapter struct {
	Name         string   `json:"name"`
	Requirements []string `json:"requirements"`
}

// Invariant is a security property and its expected enforcement boundaries.
type Invariant struct {
	ID         string   `json:"id"`
	Statement  string   `json:"statement"`
	EnforcedBy []string `json:"enforcedBy"`
}

// Contract is the complete machine-readable v0 capability contract.
type Contract struct {
	SchemaVersion    int              `json:"schemaVersion"`
	Version          string           `json:"version"`
	AuthorityClasses []AuthorityClass `json:"authorityClasses"`
	Profiles         []Profile        `json:"profiles"`
	TrustBoundaries  []TrustBoundary  `json:"trustBoundaries"`
	RequiredAdapters []Adapter        `json:"requiredAdapters"`
	Invariants       []Invariant      `json:"invariants"`
	NonGoals         []string         `json:"nonGoals"`
}

// V0 returns the embedded v0 contract after validating its invariants.
func V0() (Contract, error) {
	var result Contract
	if err := json.Unmarshal(v0JSON, &result); err != nil {
		return Contract{}, fmt.Errorf("decode embedded v0 contract: %w", err)
	}
	if err := Validate(result); err != nil {
		return Contract{}, fmt.Errorf("validate embedded v0 contract: %w", err)
	}
	return result, nil
}

// V0JSON returns a copy of the canonical JSON document.
func V0JSON() []byte {
	return slices.Clone(v0JSON)
}

// Validate checks the invariants that downstream profiles and adapters rely on.
func Validate(value Contract) error {
	if value.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1, got %d", value.SchemaVersion)
	}
	if value.Version != "v0" {
		return fmt.Errorf("version must be v0, got %q", value.Version)
	}

	authorities := make(map[string]AuthorityClass, len(value.AuthorityClasses))
	for _, authority := range value.AuthorityClasses {
		if authority.Name == "" || authority.Description == "" {
			return errors.New("authority classes require a name and description")
		}
		if _, exists := authorities[authority.Name]; exists {
			return fmt.Errorf("duplicate authority class %q", authority.Name)
		}
		if authority.ExternalMutation || authority.ProductionAccess {
			return fmt.Errorf("v0 authority class %q exceeds the non-mutating, non-production ceiling", authority.Name)
		}
		authorities[authority.Name] = authority
	}

	for name, rank := range map[string]int{"workspace-only": 0, "read-only": 1} {
		authority, exists := authorities[name]
		if !exists {
			return fmt.Errorf("missing required authority class %q", name)
		}
		if authority.Rank != rank {
			return fmt.Errorf("authority class %q must have rank %d", name, rank)
		}
	}

	profiles := make(map[string]Profile, len(value.Profiles))
	for _, profile := range value.Profiles {
		if profile.Name == "" || profile.Description == "" {
			return errors.New("profiles require a name and description")
		}
		if _, exists := profiles[profile.Name]; exists {
			return fmt.Errorf("duplicate profile %q", profile.Name)
		}
		if _, exists := authorities[profile.AuthorityCeiling]; !exists {
			return fmt.Errorf("profile %q references unknown authority class %q", profile.Name, profile.AuthorityCeiling)
		}
		if profile.RepositorySelection != "explicit" {
			return fmt.Errorf("profile %q must select repositories explicitly", profile.Name)
		}
		if profile.RemoteGitPush {
			return fmt.Errorf("profile %q must prohibit remote Git push in v0", profile.Name)
		}
		if !profile.DefaultDenyEgress {
			return fmt.Errorf("profile %q must default-deny egress", profile.Name)
		}
		if len(profile.AllowedCapabilities) == 0 || len(profile.EgressPurposes) == 0 || len(profile.ForbiddenCapabilities) == 0 {
			return fmt.Errorf("profile %q must declare allowed, egress, and forbidden capabilities", profile.Name)
		}
		if !slices.Contains(profile.ForbiddenCapabilities, "production-access") {
			return fmt.Errorf("profile %q must explicitly forbid production access", profile.Name)
		}
		profiles[profile.Name] = profile
	}

	for name, ceiling := range map[string]string{"core": "workspace-only", "platform-readonly": "read-only"} {
		profile, exists := profiles[name]
		if !exists {
			return fmt.Errorf("missing required profile %q", name)
		}
		if profile.AuthorityCeiling != ceiling {
			return fmt.Errorf("profile %q must use authority ceiling %q", name, ceiling)
		}
	}

	if err := validateNamedEntries("trust boundary", trustBoundaryNames(value.TrustBoundaries)); err != nil {
		return err
	}
	if err := validateNamedEntries("required adapter", adapterNames(value.RequiredAdapters)); err != nil {
		return err
	}
	if err := validateNamedEntries("invariant", invariantIDs(value.Invariants)); err != nil {
		return err
	}

	if err := requireEntries("trust boundary", trustBoundaryNames(value.TrustBoundaries), []string{
		"browser-client",
		"client-access-adapter",
		"external-services",
		"kubernetes-control-plane",
		"repository-content",
		"workspace-runtime",
	}); err != nil {
		return err
	}
	if err := requireEntries("adapter", adapterNames(value.RequiredAdapters), []string{
		"client-access",
		"identity-and-egress",
		"kubernetes-workspace",
		"lifecycle-and-audit",
		"provider-authentication",
		"repository-source",
		"workspace-storage",
	}); err != nil {
		return err
	}

	for _, boundary := range value.TrustBoundaries {
		if boundary.Role == "" || boundary.Boundary == "" {
			return fmt.Errorf("trust boundary %q requires a role and boundary statement", boundary.Name)
		}
	}
	for _, adapter := range value.RequiredAdapters {
		if len(adapter.Requirements) == 0 {
			return fmt.Errorf("required adapter %q has no requirements", adapter.Name)
		}
	}
	for _, invariant := range value.Invariants {
		if invariant.Statement == "" || len(invariant.EnforcedBy) == 0 {
			return fmt.Errorf("invariant %q requires a statement and enforcement boundary", invariant.ID)
		}
	}

	if err := requireEntries("invariant", invariantIDs(value.Invariants), []string{
		"authority-monotonicity",
		"default-deny-egress",
		"explicit-repositories",
		"no-ambient-credentials",
		"restricted-pod",
		"single-writer",
	}); err != nil {
		return err
	}

	if len(value.NonGoals) == 0 {
		return errors.New("v0 must declare explicit non-goals")
	}
	for _, nonGoal := range value.NonGoals {
		if nonGoal == "" {
			return errors.New("v0 non-goals cannot be empty")
		}
	}
	return nil
}

func validateNamedEntries(kind string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("%s requires a name", kind)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate %s %q", kind, name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) == 0 {
		return fmt.Errorf("at least one %s is required", kind)
	}
	return nil
}

func requireEntries(kind string, actual, required []string) error {
	for _, name := range required {
		if !slices.Contains(actual, name) {
			return fmt.Errorf("missing required %s %q", kind, name)
		}
	}
	return nil
}

func trustBoundaryNames(values []TrustBoundary) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}

func adapterNames(values []Adapter) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}

func invariantIDs(values []Invariant) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID)
	}
	return result
}
