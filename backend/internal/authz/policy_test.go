package authz

import (
	"ai-buddy/backend/platform/demo"
	"testing"
	"time"
)

func TestScopeUsesValuesAndAllAuthorizationFields(t *testing.T) {
	original := demo.Profiles()[0]
	copy := original
	team := *original.TeamID
	copy.TeamID = &team
	if !SameScope(original, copy) {
		t.Fatal("equal team values treated as different scopes")
	}
	for name, change := range map[string]func(*demo.Profile){
		"id":           func(p *demo.Profile) { p.ID = "maxim" },
		"role":         func(p *demo.Profile) { p.Role = "buddy" },
		"version":      func(p *demo.Profile) { p.PolicyVersion++ },
		"disabled":     func(p *demo.Profile) { p.Enabled = false },
		"start":        func(p *demo.Profile) { p.StartDate = "2027-01-01" },
		"team":         func(p *demo.Profile) { team := "search"; p.TeamID = &team },
		"missing team": func(p *demo.Profile) { p.TeamID = nil },
		"buddy":        func(p *demo.Profile) { p.BuddyID = nil },
		"manager":      func(p *demo.Profile) { p.ManagerID = nil },
		"curator":      func(p *demo.Profile) { p.CuratorID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			if SameScope(original, changed) || SameScope(changed, original) {
				t.Fatal("changed scope accepted")
			}
		})
	}
	p := demo.Profiles()[6]
	if !SameScope(p, p) {
		t.Fatal("nil team scope rejected")
	}
}

func TestServiceAction(t *testing.T) {
	now := time.Date(2026, 11, 12, 19, 30, 0, 0, time.UTC)
	loc := time.FixedZone("demo", 5*3600)
	for _, p := range demo.Profiles() {
		err := ServiceAction(p, now, loc)
		if (err == nil) != (p.Role == "intern") {
			t.Fatal(p, err)
		}
	}
	p := demo.Profiles()[0]
	p.StartDate = "2026-11-13"
	if err := ServiceAction(p, now, loc); err != nil {
		t.Fatal("local date ignored", err)
	}
	p.StartDate = "2026-11-14"
	if ServiceAction(p, now, loc) != ErrBeforeStart {
		t.Fatal("future start allowed")
	}
	p.Enabled = false
	if ServiceAction(p, now, loc) != ErrNotAllowed {
		t.Fatal("disabled")
	}
}
func TestResourceAndDocumentPermissions(t *testing.T) {
	p := demo.Profiles()[0]
	for _, r := range demo.Resources() {
		err := Resource(p, r)
		switch r.Kind {
		case "hidden":
			if err != ErrNotFound {
				t.Fatal(err)
			}
		case "closed":
			if err != ErrNotAllowed {
				t.Fatal(err)
			}
		default:
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, d := range demo.Documents() {
		want := d.TeamID == nil || *d.TeamID == "payments"
		if Document(p, d) != want {
			t.Fatal(d)
		}
	}
	p.TeamID = nil
	for _, d := range demo.Documents() {
		if Document(p, d) != (d.TeamID == nil) {
			t.Fatal(d)
		}
	}
	p.Role = "unknown"
	if Document(p, demo.Documents()[0]) {
		t.Fatal("unknown role")
	}
	p.Enabled = false
	if Resource(p, demo.Resources()[0]) != ErrNotFound || Document(p, demo.Documents()[0]) {
		t.Fatal("disabled")
	}
}
func TestPlanPermissionsAndRevocation(t *testing.T) {
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	p := demo.Profiles()[0]
	steps := demo.Steps()
	for _, profile := range demo.Profiles() {
		for _, step := range Steps(profile, steps) {
			if step.OwnerID != profile.ID && !(profile.Role == "intern" && step.InternID == profile.ID) {
				t.Fatal("foreign plan", profile, step)
			}
		}
	}
	find := func(id string) demo.Step {
		for _, s := range steps {
			if s.ID == id {
				return s
			}
		}
		t.Fatal(id)
		return demo.Step{}
	}
	own := find("alina-P5")
	if err := ChangeStep(p, own, steps, now, time.UTC); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id  string
		err error
	}{{"maxim-P5", ErrNotFound}, {"alina-P1", ErrNotFound}, {"alina-P6", ErrNotAllowed}, {"alina-P7", ErrDependency}} {
		if err := ChangeStep(p, find(tc.id), steps, now, time.UTC); err != tc.err {
			t.Fatal(tc, err)
		}
	}
	for i := range steps {
		if steps[i].ID == "alina-P6" {
			steps[i].Status = "done"
		}
	}
	if err := ChangeStep(p, find("alina-P7"), steps, now, time.UTC); err != nil {
		t.Fatal(err)
	}
	p.StartDate = "2026-11-13"
	if ChangeStep(p, own, steps, now, time.UTC) != ErrBeforeStart {
		t.Fatal("future")
	}
	p.Enabled = false
	if len(Steps(p, steps)) != 0 || ChangeStep(p, own, steps, now, time.UTC) != ErrNotFound {
		t.Fatal("rights not rechecked")
	}
	p = demo.Profiles()[0]
	p.TeamID = nil
	for _, s := range Steps(p, steps) {
		if s.Code == "TEAM" {
			t.Fatal("team step without team")
		}
	}
}
