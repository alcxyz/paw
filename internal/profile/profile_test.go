package profile

import "testing"

func TestBuiltinsReturnsCopy(t *testing.T) {
	first := Builtins()
	second := Builtins()

	if len(first) != 2 {
		t.Fatalf("expected two built-in profiles, got %d", len(first))
	}

	first[0].Name = "changed"
	first[0].AllowedCapabilities[0] = "changed"
	if second[0].Name != "core" {
		t.Fatalf("Builtins returned shared state: got %q", second[0].Name)
	}
	if second[0].AllowedCapabilities[0] == "changed" {
		t.Fatal("Builtins returned shared capability state")
	}
}

func TestLookupReturnsEffectiveReadonlyAuthority(t *testing.T) {
	definition, exists := Lookup("platform-readonly")
	if !exists {
		t.Fatal("platform-readonly was not found")
	}
	if definition.Authority != "read-only" || definition.ExternalMutation || definition.ProductionAccess {
		t.Fatalf("unexpected authority: %#v", definition)
	}
	if !definition.PlatformIdentity || definition.RemoteGitPush || !definition.DefaultDenyEgress {
		t.Fatalf("unexpected boundary: %#v", definition)
	}
}
