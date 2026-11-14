package api

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/internal/onboarding"
	"ai-buddy/backend/platform/demo"
	"context"
	"net/http"
)

type PlanStore interface {
	Steps(context.Context, string) ([]demo.Step, error)
}

func (s *Server) onboarding(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	store, ok := s.identities.(PlanStore)
	if !ok {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	steps, err := store.Steps(r.Context(), profile.ID)
	if err != nil {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	current, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if !authz.SameScope(profile, current) {
		failure(w, 403, "NOT_ALLOWED")
		return
	}
	plan := onboarding.Project(profile, steps, s.config.Clock.Now(), s.config.Location)
	if b := s.config.Business; b != nil {
		objects, err := b.Store.Objects(r.Context(), profile.ID)
		if err != nil {
			s.businessFailure(w, err)
			return
		}
		resources, err := b.Store.Resources(r.Context(), profile)
		if err != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		for _, object := range objects {
			visible := object.Service == "time_off"
			for _, resource := range resources {
				if resource.ID == object.ResourceID {
					visible = true
				}
			}
			if visible {
				plan.Requests = append(plan.Requests, object)
			}
		}
	}
	respond(w, 200, plan)
}
