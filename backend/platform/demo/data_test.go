package demo

import (
	"encoding/json"
	"testing"
)

func TestFixtures(t *testing.T) {
	profiles := Profiles()
	if len(profiles) != 7 {
		t.Fatal(profiles)
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		if seen[p.ID] {
			t.Fatal("duplicate")
		}
		seen[p.ID] = true
	}
	for _, s := range Steps() {
		if !seen[s.OwnerID] || !seen[s.InternID] || s.Version != 1 {
			t.Fatal(s)
		}
	}
	for _, d := range Documents() {
		if !json.Valid(d.Fragments) || !d.Demo || !seen[d.OwnerID] {
			t.Fatal(d)
		}
	}
	if len(Resources()) != 11 {
		t.Fatal("catalogue incomplete")
	}
}
