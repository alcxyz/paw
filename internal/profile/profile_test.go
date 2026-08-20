package profile

import "testing"

func TestBuiltinsReturnsCopy(t *testing.T) {
	first := Builtins()
	second := Builtins()

	if len(first) != 2 {
		t.Fatalf("expected two built-in profiles, got %d", len(first))
	}

	first[0].Name = "changed"
	if second[0].Name != "core" {
		t.Fatalf("Builtins returned shared state: got %q", second[0].Name)
	}
}
