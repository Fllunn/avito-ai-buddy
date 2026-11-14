package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/internal/integrations"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/internal/tools"
	"ai-buddy/backend/platform/calendar"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Business owns runtime dependencies; identity remains the session's authority.
type Business struct {
	Store                        *storage.Store
	Tools                        tools.Client
	Builder                      agent.Builder
	Provider                     agent.Provider
	LLMTimeout, CardTTL, StepTTL time.Duration
}

func (s *Server) businessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/chat", s.chat)
	mux.HandleFunc("GET /api/executions", s.executions)
	mux.HandleFunc("GET /api/executions/{id}", s.execution)
	mux.HandleFunc("GET /demo/docs/{id}", s.document)
	mux.HandleFunc("POST /api/chat/messages", s.message)
	mux.HandleFunc("POST /api/dialogue/actions", s.button)
	mux.HandleFunc("POST /api/cards", s.createCard)
	mux.HandleFunc("GET /api/cards/{id}", s.getCard)
	mux.HandleFunc("PATCH /api/cards/{id}", s.editCard)
	mux.HandleFunc("POST /api/cards/{id}/discard", s.discardCard)
	mux.HandleFunc("POST /api/cards/{id}/confirm", s.confirmCard)
	mux.HandleFunc("GET /api/attempts/{id}", s.getAttempt)
	mux.HandleFunc("POST /api/attempts/{id}/reconcile", s.reconcile)
	mux.HandleFunc("POST /api/attempts/{id}/retry", s.retry)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/requests/refresh", s.refresh)
	mux.HandleFunc("POST /api/steps/{id}/confirmation", s.stepConfirmation)
	mux.HandleFunc("POST /api/steps/{id}/confirm", s.confirmStep)
}
func (s *Server) businessFailure(w http.ResponseWriter, err error) {
	code, status := "STORAGE_UNAVAILABLE", 503
	var remote integrations.Error
	var validation actions.ValidationError
	var rejected reapplyError
	if errors.As(err, &rejected) {
		respond(w, 409, map[string]any{"error": map[string]any{"code": "REAPPLY_CONFIRMATION_REQUIRED", "fields": map[string]string{"previous_id": rejected.ID, "reason": rejected.Reason, "advice": "Обсуди отказ с руководителем или наставником. Повторная подача требует явного продолжения."}, "retryable": false}, "request_id": actions.ID()})
		return
	}
	var form tools.FormError
	if errors.As(err, &form) {
		respond(w, 400, map[string]any{"error": map[string]any{"code": "VALIDATION_ERROR", "fields": form.Fields, "retryable": false}, "request_id": actions.ID()})
		return
	}
	switch {
	case errors.Is(err, actions.ErrMissing) || errors.Is(err, authz.ErrNotFound):
		code, status = "NOT_FOUND", 404
	case errors.Is(err, actions.ErrInvalid):
		code, status = "VALIDATION_ERROR", 400
	case errors.Is(err, authz.ErrNotAllowed) || errors.Is(err, authz.ErrBeforeStart) || errors.Is(err, authz.ErrDependency):
		code, status = "NOT_ALLOWED", 403
	case errors.Is(err, actions.ErrMessageConflict):
		code, status = "MESSAGE_ID_CONFLICT", 409
	case errors.Is(err, actions.ErrConflict):
		code, status = "VERSION_CONFLICT", 409
	case errors.Is(err, actions.ErrExpired):
		code, status = "CARD_EXPIRED", 409
	case errors.Is(err, actions.ErrActive):
		code, status = "CONFLICTING_ATTEMPT", 409
	case errors.As(err, &remote):
		code, status = remote.Code, remote.Status
		if status < 400 || status > 599 {
			code, status = "SERVICE_UNAVAILABLE", 503
		}
	case errors.As(err, &validation):
		if !validation.Retryable {
			code, status = "NOT_ALLOWED", 403
		}
	}
	failure(w, status, code)
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	after := int64(0)
	if r.URL.Query().Has("after") {
		var e error
		after, e = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if e != nil || after < 0 {
			failure(w, 400, "VALIDATION_ERROR")
			return
		}
	}
	b := s.config.Business
	if e := b.Store.Welcome(r.Context(), p, s.config.Clock.Now()); e != nil {
		s.businessFailure(w, e)
		return
	}
	items, e := b.Store.Messages(r.Context(), p.ID, after)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	cards, e := b.Store.Cards(r.Context(), p.ID, s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	frame, e := b.localFrame(r.Context(), p.ID)
	if e != nil {
		failure(w, 503, "CONTEXT_UNAVAILABLE")
		return
	}
	items = agent.AuthorizedHistory(frame, items)
	visibleCards := []actions.Card{}
	for _, c := range cards {
		if decorated, e := b.decorate(r.Context(), p, c); e == nil {
			visibleCards = append(visibleCards, decorated)
		}
	}
	cards = visibleCards
	next := after
	for _, m := range items {
		next = m.Sequence
	}
	respond(w, 200, map[string]any{"items": items, "next_cursor": next, "active_cards": cards, "llm_mode": b.Provider.Mode()})
}
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		ID   string `json:"client_message_id"`
		Text string `json:"text"`
	}
	if !decode(w, r, &input) {
		return
	}
	clean := safety.Prepare(r.Context(), safety.Local{}, input.Text)
	m, _, e := s.config.Business.Store.AcceptMessage(r.Context(), p.ID, input.ID, "text", "", clean, s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	job, e := s.config.Business.Store.MessageJob(r.Context(), p.ID, m.ID)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	ids, e := s.config.Business.Store.ExecutionIDs(r.Context(), p.ID, "job", job)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]any{"message_id": m.ID, "job_id": job, "execution_ids": ids})
}
func (s *Server) button(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		ID     string      `json:"client_message_id"`
		Intent string      `json:"intent"`
		Slots  agent.Slots `json:"slots"`
	}
	if !decode(w, r, &input) {
		return
	}
	slots := actions.Slots{ResourceID: input.Slots.ResourceID, Level: input.Slots.Level, StartDate: input.Slots.StartDate, EndDate: input.Slots.EndDate, RequestID: input.Slots.RequestID, StepID: input.Slots.StepID, Query: safety.Prepare(r.Context(), safety.Local{}, input.Slots.Query), Justification: safety.Prepare(r.Context(), safety.Local{}, input.Slots.Justification)}
	m, _, e := s.config.Business.Store.AcceptAction(r.Context(), p.ID, input.ID, input.Intent, slots, s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	job, e := s.config.Business.Store.MessageJob(r.Context(), p.ID, m.ID)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	ids, e := s.config.Business.Store.ExecutionIDs(r.Context(), p.ID, "job", job)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]any{"job_id": job, "message_ids": []int64{m.ID}, "execution_ids": ids})
}
func (s *Server) prepare(ctx context.Context, p demo.Profile, kind string, raw json.RawMessage) (json.RawMessage, actions.Reservation, error) {
	b := s.config.Business
	clean, reservation, e := b.Tools.Prepare(ctx, p, kind, raw)
	if e != nil {
		return nil, reservation, e
	}
	if e = b.Store.Conflict(ctx, p.ID, reservation); e != nil {
		return nil, reservation, e
	}
	return clean, reservation, nil
}
func (s *Server) createCard(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		Type       string          `json:"type"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if !decode(w, r, &input) {
		return
	}
	clean, _, e := s.prepare(r.Context(), p, input.Type, input.Parameters)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	if input.Type == "access_create" || input.Type == "time_off_create" {
		params, _ := tools.Decode(input.Type, clean)
		prior, e := s.config.Business.priorRejection(r.Context(), p, input.Type, params)
		if e != nil {
			s.businessFailure(w, e)
			return
		}
		if prior != nil {
			s.businessFailure(w, reapplyError{prior.ID, rejectionReason(prior)})
			return
		}
	}
	c, e := s.config.Business.Store.CreateCard(r.Context(), p.ID, input.Type, clean, s.config.Clock.Now(), s.config.Business.CardTTL)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	c, e = s.config.Business.decorate(r.Context(), p, c)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"card": c})
}
func (s *Server) ownedCard(w http.ResponseWriter, r *http.Request) (demo.Profile, actions.Card, bool) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return p, actions.Card{}, false
	}
	c, e := s.config.Business.Store.Card(r.Context(), p.ID, r.PathValue("id"), s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return p, c, false
	}
	return p, c, true
}
func (s *Server) getCard(w http.ResponseWriter, r *http.Request) {
	p, c, ok := s.ownedCard(w, r)
	if ok {
		c, e := s.config.Business.decorate(r.Context(), p, c)
		if e != nil {
			s.businessFailure(w, e)
			return
		}
		respond(w, 200, map[string]any{"card": c})
	}
}
func (s *Server) editCard(w http.ResponseWriter, r *http.Request) {
	p, c, ok := s.ownedCard(w, r)
	if !ok {
		return
	}
	var input struct {
		Version int                        `json:"expected_version"`
		Changes map[string]json.RawMessage `json:"changes"`
	}
	if !decode(w, r, &input) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(c.Parameters, &fields) != nil {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	for k, v := range input.Changes {
		fields[k] = v
	}
	raw, _ := json.Marshal(fields)
	clean, _, e := s.prepare(r.Context(), p, c.Type, raw)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	oldParams, _ := tools.Decode(c.Type, c.Parameters)
	newParams, _ := tools.Decode(c.Type, clean)
	if c.Version != input.Version {
		s.businessFailure(w, actions.ErrConflict)
		return
	}
	if c.Type == "access_create" && oldParams.ResourceID != newParams.ResourceID {
		c, e = s.config.Business.Store.CreateCard(r.Context(), p.ID, c.Type, clean, s.config.Clock.Now(), s.config.Business.CardTTL)
	} else {
		c, e = s.config.Business.Store.EditCard(r.Context(), p.ID, c.ID, input.Version, clean, s.config.Clock.Now(), s.config.Business.CardTTL)
	}
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"card": c})
}
func (s *Server) discardCard(w http.ResponseWriter, r *http.Request) {
	p, c, ok := s.ownedCard(w, r)
	if !ok {
		return
	}
	var input struct {
		Version int `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	c, e := s.config.Business.Store.CancelCard(r.Context(), p.ID, c.ID, input.Version, s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"card": c})
}
func (s *Server) cardValidator(ctx context.Context, p demo.Profile, c actions.Card) (actions.Reservation, error) {
	_, reservation, e := s.config.Business.Tools.Prepare(ctx, p, c.Type, c.Parameters)
	return reservation, e
}
func (s *Server) confirmCard(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		Version int `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	a, e := s.config.Business.Store.ConfirmCard(r.Context(), p.ID, r.PathValue("id"), input.Version, s.config.Clock.Now(), s.config.Location, s.cardValidator)
	if e != nil {
		_ = s.config.Business.Store.ObserveCardRefusal(r.Context(), p.ID, r.PathValue("id"), input.Version)
		s.businessFailure(w, e)
		return
	}
	ids, e := s.config.Business.Store.ExecutionIDs(r.Context(), p.ID, "attempt", a.ID)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]any{"attempt_id": a.ID, "job_id": a.JobID, "state": a.State, "execution_ids": ids})
}
func (s *Server) getAttempt(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	a, e := s.config.Business.Store.Attempt(r.Context(), p.ID, r.PathValue("id"))
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, a)
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	j, e := s.config.Business.Store.Job(r.Context(), p.ID, r.PathValue("id"))
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, j)
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	id, e := s.config.Business.Store.ReconcileNow(r.Context(), p.ID, r.PathValue("id"), s.config.Clock.Now())
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]string{"job_id": id})
}
func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	a, e := s.config.Business.Store.Retry(r.Context(), p.ID, r.PathValue("id"), s.config.Clock.Now(), s.config.Location, s.cardValidator)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	ids, e := s.config.Business.Store.ExecutionIDs(r.Context(), p.ID, "attempt", a.ID)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]any{"attempt_id": a.ID, "job_id": a.JobID, "state": a.State, "execution_ids": ids})
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		Kind string `json:"kind"`
		ID   string `json:"id,omitempty"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Kind != "access" && input.Kind != "time_off" {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	if input.ID != "" {
		if _, e := s.config.Business.Tools.Remote.Get(r.Context(), p.ID, input.Kind, input.ID); e != nil {
			s.businessFailure(w, e)
			return
		}
	}
	id, e := s.config.Business.Store.RefreshJob(r.Context(), p.ID)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 202, map[string]string{"job_id": id})
}
func (s *Server) stepConfirmation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		Version int64  `json:"expected_version"`
		Target  string `json:"target_state"`
	}
	if !decode(w, r, &input) {
		return
	}
	c, e := s.config.Business.Store.StepConfirmation(r.Context(), p, r.PathValue("id"), input.Version, input.Target, s.config.Clock.Now(), s.config.Business.StepTTL, s.config.Location)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, c)
}
func (s *Server) confirmStep(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var input struct {
		ID string `json:"confirmation_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	step, e := s.config.Business.Store.ConfirmStep(r.Context(), p.ID, r.PathValue("id"), input.ID, s.config.Clock.Now(), s.config.Location)
	if e != nil {
		s.businessFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"step": step})
}

type localDirectory struct{ store *storage.Store }

func (d localDirectory) Resources(ctx context.Context, user string) ([]demo.Resource, error) {
	p, e := d.store.Profile(ctx, user)
	if e != nil {
		return nil, e
	}
	return d.store.Resources(ctx, p)
}
func (b *Business) localFrame(ctx context.Context, user string) (agent.Context, error) {
	return (agent.Builder{Source: b.Store, Directory: localDirectory{b.Store}}).Build(ctx, user)
}
func (b *Business) decorate(ctx context.Context, p demo.Profile, c actions.Card) (actions.Card, error) {
	params, e := tools.Decode(c.Type, c.Parameters)
	if e != nil {
		return c, e
	}
	summary := &actions.CardSummary{Demo: true}
	if c.Type == "access_create" {
		resources, e := b.Store.Resources(ctx, p)
		if e != nil {
			return c, e
		}
		found := false
		for _, resource := range resources {
			if resource.ID == params.ResourceID {
				if resource.Kind == "hidden" {
					return c, actions.ErrMissing
				}
				summary.ResourceName = resource.Name
				summary.ApproverID = resource.ApproverID
				if summary.ApproverID == "manager" && p.ManagerID != nil {
					summary.ApproverID = *p.ManagerID
				}
				found = true
			}
		}
		if !found {
			return c, actions.ErrMissing
		}
	}
	if c.Type == "time_off_create" {
		if p.ManagerID != nil {
			summary.ApproverID = *p.ManagerID
		}
		a, e := time.ParseInLocation(time.DateOnly, params.StartDate, b.Tools.Location)
		if e != nil {
			return c, e
		}
		z, e := time.ParseInLocation(time.DateOnly, params.EndDate, b.Tools.Location)
		if e != nil {
			return c, e
		}
		summary.Workdays = calendar.Count(a, z)
		labels := []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
		summary.StartLabel = labels[a.Weekday()] + ", " + a.Format("02.01.2006")
		summary.EndLabel = labels[z.Weekday()] + ", " + z.Format("02.01.2006")
	}
	c.Summary = summary
	return c, nil
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if s.config.Business.Store.Pool.Ping(ctx) != nil {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	if s.config.Demo {
		if _, e := s.config.Business.Tools.Remote.Resources(ctx, "alina"); e != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
	}
	respond(w, 200, map[string]string{"status": "ready"})
}
