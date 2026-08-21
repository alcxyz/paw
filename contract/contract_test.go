package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestV0IsValid(t *testing.T) {
	value, err := V0()
	if err != nil {
		t.Fatalf("V0 returned an error: %v", err)
	}
	if value.Version != "v0" {
		t.Fatalf("expected v0 contract, got %q", value.Version)
	}
}

func TestV0JSONReturnsCopy(t *testing.T) {
	first := V0JSON()
	second := V0JSON()
	first[0] = 'x'
	if first[0] == second[0] {
		t.Fatal("V0JSON returned shared mutable state")
	}
}

func TestV0JSONRejectsUnknownFields(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(string(V0JSON())))
	decoder.DisallowUnknownFields()
	var value Contract
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("canonical contract does not match Go schema: %v", err)
	}
}

func TestValidateRejectsAuthorityEscalation(t *testing.T) {
	value := mustV0(t)
	value.AuthorityClasses[1].ExternalMutation = true

	err := Validate(value)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected authority escalation error, got %v", err)
	}
}

func TestValidateRejectsAmbientRepositorySelection(t *testing.T) {
	value := mustV0(t)
	value.Profiles[0].RepositorySelection = "ambient"

	err := Validate(value)
	if err == nil || !strings.Contains(err.Error(), "explicitly") {
		t.Fatalf("expected repository selection error, got %v", err)
	}
}

func TestValidateRejectsOpenEgress(t *testing.T) {
	value := mustV0(t)
	value.Profiles[0].DefaultDenyEgress = false

	err := Validate(value)
	if err == nil || !strings.Contains(err.Error(), "default-deny") {
		t.Fatalf("expected default-deny error, got %v", err)
	}
}

func TestValidateRejectsMissingAdapter(t *testing.T) {
	value := mustV0(t)
	value.RequiredAdapters = value.RequiredAdapters[1:]

	err := Validate(value)
	if err == nil || !strings.Contains(err.Error(), "kubernetes-workspace") {
		t.Fatalf("expected missing adapter error, got %v", err)
	}
}

func mustV0(t *testing.T) Contract {
	t.Helper()
	value, err := V0()
	if err != nil {
		t.Fatalf("V0 returned an error: %v", err)
	}
	return value
}
