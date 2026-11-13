package onboarding

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"sort"
	"time"
)

type Step struct {
	demo.Step
	Blocked bool `json:"blocked"`
	Overdue bool `json:"overdue"`
	Own     bool `json:"own"`
}
type Plan struct {
	Steps           []Step `json:"steps"`
	NextSteps       []Step `json:"next_steps"`
	WaitingOnOthers []Step `json:"waiting_on_others"`
	Requests        []any  `json:"requests"`
	Week            int    `json:"week"`
	ReadOnly        bool   `json:"read_only"`
	AsOf            string `json:"as_of"`
}

func Project(profile demo.Profile, source []demo.Step, now time.Time, location *time.Location) Plan {
	start, _ := clock.Date(profile.StartDate, location)
	week := clock.Week(start, now, location)
	result := Plan{Steps: []Step{}, NextSteps: []Step{}, WaitingOnOthers: []Step{}, Requests: []any{}, Week: week, ReadOnly: profile.Role == "intern" && week == 0, AsOf: now.UTC().Format(time.RFC3339)}
	if week == 0 {
		result.Week = 1
	}
	allowed := authz.Steps(profile, source)
	done := map[string]bool{}
	unlocks := map[string]int{}
	for _, step := range allowed {
		done[step.ID] = step.Status == "done"
		for _, dependency := range step.Dependencies {
			unlocks[dependency]++
		}
	}
	for _, step := range allowed {
		blocked := false
		for _, dependency := range step.Dependencies {
			if !done[dependency] {
				blocked = true
			}
		}
		view := Step{step, blocked, step.Period < result.Week && step.Status != "done", step.OwnerID == profile.ID}
		result.Steps = append(result.Steps, view)
		if step.Status == "done" {
			continue
		}
		if !view.Own {
			if len(result.WaitingOnOthers) < 3 {
				result.WaitingOnOthers = append(result.WaitingOnOthers, view)
			}
			continue
		}
		if !blocked {
			result.NextSteps = append(result.NextSteps, view)
		}
	}
	rank := func(step Step) int {
		if step.Status == "attention" {
			return 0
		}
		if step.Overdue {
			return 1
		}
		if step.Period <= result.Week {
			if step.Status == "in_progress" {
				return 2
			}
			return 3
		}
		return 4
	}
	sort.SliceStable(result.NextSteps, func(i, j int) bool {
		a, b := result.NextSteps[i], result.NextSteps[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return unlocks[a.ID] > unlocks[b.ID]
	})
	if len(result.NextSteps) > 3 {
		result.NextSteps = result.NextSteps[:3]
	}
	return result
}
