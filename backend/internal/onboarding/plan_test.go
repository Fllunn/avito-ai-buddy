package onboarding

import (
	"ai-buddy/backend/platform/demo"
	"testing"
	"time"
)

func TestProfilesAndDependencies(t *testing.T) {
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	profiles := demo.Profiles()
	steps := demo.Steps()
	a := Project(profiles[0], steps, now, time.UTC)
	b := Project(profiles[1], steps, now, time.UTC)
	if a.Week != 1 || b.Week != 2 || len(a.WaitingOnOthers) != 3 || len(b.NextSteps) == 0 {
		t.Fatal(a, b)
	}
	if len(a.NextSteps) != 3 || a.NextSteps[0].Code != "P6" {
		t.Fatal("unlocking priority", a.NextSteps)
	}
	for _, s := range a.Steps {
		if s.ID == "alina-P7" && !s.Blocked {
			t.Fatal("P7 available without grant")
		}
	}
	for i := range steps {
		if steps[i].ID == "alina-P6" {
			steps[i].Status = "done"
		}
	}
	a = Project(profiles[0], steps, now, time.UTC)
	for _, s := range a.Steps {
		if s.ID == "alina-P7" && s.Blocked {
			t.Fatal("approved did not open P7")
		}
	}
	p := profiles[0]
	p.StartDate = "2026-11-13"
	a = Project(p, steps, now, time.UTC)
	if !a.ReadOnly || a.Week != 1 {
		t.Fatal(a)
	}
	p.TeamID = nil
	a = Project(p, steps, now, time.UTC)
	for _, s := range a.Steps {
		if s.Code == "TEAM" {
			t.Fatal("unknown team")
		}
	}
	for _, profile := range profiles[2:] {
		plan := Project(profile, steps, now, time.UTC)
		for _, s := range plan.Steps {
			if !s.Own || s.OwnerID != profile.ID {
				t.Fatal("foreign assignment")
			}
		}
	}
}
func TestPriorityAndEmptyPlan(t *testing.T) {
	p := demo.Profiles()[0]
	now := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)
	source := []demo.Step{}
	for i, status := range []string{"not_started", "in_progress", "attention", "not_started", "not_started"} {
		period := 2
		if i == 3 {
			period = 1
		}
		if i == 4 {
			period = 3
		}
		source = append(source, demo.Step{ID: []string{"normal", "progress", "attention", "overdue", "future"}[i], InternID: p.ID, OwnerID: p.ID, Status: status, Period: period, Dependencies: []string{}})
	}
	plan := Project(p, source, now, time.UTC)
	for i, id := range []string{"attention", "overdue", "progress"} {
		if plan.NextSteps[i].ID != id {
			t.Fatal(plan.NextSteps)
		}
	}
	plan = Project(p, nil, now, time.UTC)
	if len(plan.NextSteps) != 0 || plan.NextSteps == nil || plan.WaitingOnOthers == nil {
		t.Fatal(plan)
	}
}
