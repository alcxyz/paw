package profile

import (
	"slices"

	"git.alc.xyz/alcxyz/paw/contract"
)

// Definition is the user-visible portion of a built-in workspace profile.
// Runtime enforcement belongs to the emitted Kubernetes and identity contracts.
type Definition struct {
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Authority             string   `json:"authorityCeiling"`
	AuthorityRank         int      `json:"authorityRank"`
	ExternalMutation      bool     `json:"externalMutation"`
	ProductionAccess      bool     `json:"productionAccess"`
	RepositorySelection   string   `json:"repositorySelection"`
	RemoteGitPush         bool     `json:"remoteGitPush"`
	DefaultDenyEgress     bool     `json:"defaultDenyEgress"`
	PlatformIdentity      bool     `json:"platformIdentityRequired"`
	AllowedCapabilities   []string `json:"allowedCapabilities"`
	EgressPurposes        []string `json:"egressPurposes"`
	ForbiddenCapabilities []string `json:"forbiddenCapabilities"`
	RequiredAdapters      []string `json:"requiredAdapters"`
}

// Builtins returns definitions derived from the canonical embedded contract.
func Builtins() []Definition {
	value, err := contract.V0()
	if err != nil {
		panic(err)
	}

	authorities := make(map[string]contract.AuthorityClass, len(value.AuthorityClasses))
	for _, authority := range value.AuthorityClasses {
		authorities[authority.Name] = authority
	}
	requiredAdapters := make([]string, 0, len(value.RequiredAdapters))
	for _, adapter := range value.RequiredAdapters {
		requiredAdapters = append(requiredAdapters, adapter.Name)
	}

	result := make([]Definition, 0, len(value.Profiles))
	for _, contractProfile := range value.Profiles {
		authority := authorities[contractProfile.AuthorityCeiling]
		result = append(result, Definition{
			Name:                  contractProfile.Name,
			Description:           contractProfile.Description,
			Authority:             contractProfile.AuthorityCeiling,
			AuthorityRank:         authority.Rank,
			ExternalMutation:      authority.ExternalMutation,
			ProductionAccess:      authority.ProductionAccess,
			RepositorySelection:   contractProfile.RepositorySelection,
			RemoteGitPush:         contractProfile.RemoteGitPush,
			DefaultDenyEgress:     contractProfile.DefaultDenyEgress,
			PlatformIdentity:      contractProfile.Name == "platform-readonly",
			AllowedCapabilities:   slices.Clone(contractProfile.AllowedCapabilities),
			EgressPurposes:        slices.Clone(contractProfile.EgressPurposes),
			ForbiddenCapabilities: slices.Clone(contractProfile.ForbiddenCapabilities),
			RequiredAdapters:      slices.Clone(requiredAdapters),
		})
	}
	return result
}

// Lookup returns one built-in profile by its canonical name.
func Lookup(name string) (Definition, bool) {
	for _, definition := range Builtins() {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}
