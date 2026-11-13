package api

import (
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
	respond(w, 200, onboarding.Project(profile, steps, s.config.Clock.Now(), s.config.Location))
}
