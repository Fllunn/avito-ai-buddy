// Package authz implements server-side data and action permissions.
package authz

import (
	"ai-buddy/backend/platform/demo"
	"errors"
	"slices"
	"time"
)

var ErrUnauthenticated = errors.New("UNAUTHENTICATED")
var ErrNotFound = errors.New("NOT_FOUND")
var ErrNotAllowed = errors.New("NOT_ALLOWED")
var ErrBeforeStart = errors.New("BEFORE_START")
var ErrDependency = errors.New("DEPENDENCY_NOT_DONE")

func ServiceAction(p demo.Profile, now time.Time, location *time.Location) error {
	if !p.Enabled || p.Role != "intern" {
		return ErrNotAllowed
	}
	if now.In(location).Format(time.DateOnly) < p.StartDate {
		return ErrBeforeStart
	}
	return nil
}
func Resource(p demo.Profile, r demo.Resource) error {
	if !p.Enabled || r.Kind == "hidden" {
		return ErrNotFound
	}
	if r.Kind == "closed" {
		return ErrNotAllowed
	}
	return nil
}
func Document(p demo.Profile, d demo.Document) bool {
	return p.Enabled && slices.Contains(d.Roles, p.Role) && (d.TeamID == nil || p.TeamID != nil && *p.TeamID == *d.TeamID)
}
func Steps(p demo.Profile, steps []demo.Step) []demo.Step {
	result := []demo.Step{}
	if !p.Enabled {
		return result
	}
	for _, step := range steps {
		if step.OwnerID == p.ID || p.Role == "intern" && step.InternID == p.ID {
			if p.TeamID == nil && step.Code == "TEAM" {
				continue
			}
			result = append(result, step)
		}
	}
	return result
}
func ChangeStep(p demo.Profile, step demo.Step, steps []demo.Step, now time.Time, location *time.Location) error {
	if !p.Enabled || step.OwnerID != p.ID {
		return ErrNotFound
	}
	if step.Closure != "manual" {
		return ErrNotAllowed
	}
	if p.Role == "intern" {
		if err := ServiceAction(p, now, location); err != nil {
			return err
		}
	}
	for _, dependency := range step.Dependencies {
		found := false
		for _, candidate := range steps {
			if candidate.ID == dependency && candidate.Status == "done" {
				found = true
				break
			}
		}
		if !found {
			return ErrDependency
		}
	}
	return nil
}
