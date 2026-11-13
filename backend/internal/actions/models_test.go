package actions

import "testing"

func TestIdentifiers(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		value := ID()
		if !ValidID(value) || seen[value] {
			t.Fatal(value)
		}
		seen[value] = true
	}
	for _, bad := range []string{"", "alina", "00000000-0000-0000-0000-000000000000"} {
		if ValidID(bad) {
			t.Fatal(bad)
		}
	}
}
