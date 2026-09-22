package deployment

import (
	"fmt"
	"strings"
)

// EgressDestination is one approved hostname and the declared purpose that
// admits it. The per-workspace egress proxy allows CONNECT on port 443 to
// exactly these hostnames and nothing else.
type EgressDestination struct {
	Host    string
	Purpose string
}

// PurposeApprovedProviderAPI is the contract egress purpose for the selected
// provider's API and authentication endpoints.
const PurposeApprovedProviderAPI = "approved-provider-api"

// providerDestinations is the reviewed resolution table from provider to
// destinations. A provider that is absent here, or maps to nil, gets no
// external egress at all: its workspace keeps default deny and the provider
// tool cannot reach its service until a reviewed entry is added.
var providerDestinations = map[string][]EgressDestination{
	"none": nil,
	// Codex with a ChatGPT login: device and browser authentication run
	// against auth.openai.com, conversations use the chatgpt.com backend,
	// and API-key sessions use api.openai.com.
	"codex": {
		{Host: "auth.openai.com", Purpose: PurposeApprovedProviderAPI},
		{Host: "chatgpt.com", Purpose: PurposeApprovedProviderAPI},
		{Host: "api.openai.com", Purpose: PurposeApprovedProviderAPI},
	},
	"claude-code": nil,
	"opencode":    nil,
}

// EgressDestinations resolves the reviewed destination list for a selection.
func EgressDestinations(selection Selection) ([]EgressDestination, error) {
	if _, err := ImageName(selection); err != nil {
		return nil, err
	}
	destinations, exists := providerDestinations[selection.Provider]
	if !exists {
		return nil, fmt.Errorf("provider %q has no reviewed egress destinations", selection.Provider)
	}
	result := make([]EgressDestination, len(destinations))
	copy(result, destinations)
	return result, nil
}

// EgressDestinationsText renders destinations in the proxy's list format: one
// "<host> <purpose>" entry per line.
func EgressDestinationsText(destinations []EgressDestination) string {
	var builder strings.Builder
	for _, destination := range destinations {
		builder.WriteString(destination.Host)
		builder.WriteString(" ")
		builder.WriteString(destination.Purpose)
		builder.WriteString("\n")
	}
	return builder.String()
}
