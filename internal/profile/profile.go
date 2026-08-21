package profile

import "git.alc.xyz/alcxyz/paw/contract"

// Definition is the user-visible portion of a built-in workspace profile.
// Runtime enforcement belongs to the emitted Kubernetes and identity contracts.
type Definition struct {
	Name        string
	Authority   string
	Description string
}

// Builtins returns definitions derived from the canonical embedded contract.
func Builtins() []Definition {
	value, err := contract.V0()
	if err != nil {
		panic(err)
	}

	result := make([]Definition, 0, len(value.Profiles))
	for _, profile := range value.Profiles {
		result = append(result, Definition{
			Name:        profile.Name,
			Authority:   profile.AuthorityCeiling,
			Description: profile.Description,
		})
	}
	return result
}
