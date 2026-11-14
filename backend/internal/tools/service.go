// Package tools validates tool schemas and server-created confirmation contexts.
package tools

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/internal/integrations"
	"ai-buddy/backend/internal/onboarding"
	"ai-buddy/backend/platform/calendar"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Authority interface {
	Attempt(context.Context, string, string) (actions.Attempt, error)
	Profile(context.Context, string) (demo.Profile, error)
}
type Client struct {
	Authority Authority
	Source    agent.Source
	Directory agent.Directory
	Remote    integrations.Client
	Clock     clock.Clock
	Location  *time.Location
}
type FormError struct{ Fields map[string]string }

func (FormError) Error() string { return "VALIDATION_ERROR" }

type Parameters struct {
	ResourceID    string `json:"resource_id,omitempty"`
	Level         string `json:"level,omitempty"`
	Justification string `json:"justification,omitempty"`
	StartDate     string `json:"start_date,omitempty"`
	EndDate       string `json:"end_date,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	Version       int64  `json:"expected_version,omitempty"`
}
type Schema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"inputSchema"`
	Output      json.RawMessage `json:"outputSchema"`
	Mutates     bool            `json:"mutates"`
}

func Schemas() []Schema {
	return []Schema{

		{"onboarding.get_plan", "Прочитать собственный план", json.RawMessage(`{"type":"object","additionalProperties":false}`), json.RawMessage(`{"type":"object","required":["steps","next_steps","waiting_on_others","as_of"]}`), false},
		{"documents.search", "Найти разрешённые документы", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","maxLength":4000}}}`), json.RawMessage(`{"type":"object","required":["items"]}`), false},
		{"access.list_resources", "Прочитать видимый каталог", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"}}}`), json.RawMessage(`{"type":"object","required":["items"]}`), false},
		{"access.get_status", "Прочитать собственную заявку", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_id"],"properties":{"request_id":{"type":"string"}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), false},
		{"time_off.get_status", "Прочитать собственный отгул", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_id"],"properties":{"request_id":{"type":"string"}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), false},
		{"access.create_request", "Создать собственную заявку после подтверждения", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["resource_id","level","justification"],"properties":{"resource_id":{"type":"string"},"level":{"enum":["read","edit","write"]},"justification":{"type":"string","minLength":10,"maxLength":500}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), true},
		{"access.cancel_request", "Отменить собственную заявку после подтверждения", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_id","expected_version"],"properties":{"request_id":{"type":"string"},"expected_version":{"type":"integer","minimum":1}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), true},
		{"time_off.create_request", "Создать собственный отгул без причины", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["start_date","end_date"],"properties":{"start_date":{"type":"string","format":"date"},"end_date":{"type":"string","format":"date"}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), true},
		{"time_off.cancel_request", "Отменить собственный отгул до начала", json.RawMessage(`{"type":"object","additionalProperties":false,"required":["request_id","expected_version"],"properties":{"request_id":{"type":"string"},"expected_version":{"type":"integer","minimum":1}}}`), json.RawMessage(`{"type":"object","required":["id","user_id","status","version"]}`), true},
	}
}
func Decode(kind string, raw json.RawMessage) (Parameters, error) {
	var p Parameters
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 8192 || decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF {
		return p, actions.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return p, actions.ErrInvalid
	}
	allowed := []string{}
	switch kind {
	case "access_create":
		allowed = []string{"resource_id", "level", "justification"}
	case "time_off_create":
		allowed = []string{"start_date", "end_date"}
	case "access_cancel", "time_off_cancel":
		allowed = []string{"request_id", "expected_version"}
	default:
		return p, actions.ErrInvalid
	}
	for key := range fields {
		if !slices.Contains(allowed, key) || string(fields[key]) == "null" {
			return p, actions.ErrInvalid
		}
	}
	if len(fields) != len(allowed) {
		return p, actions.ErrInvalid
	}
	return p, nil
}
func (c Client) Prepare(ctx context.Context, p demo.Profile, kind string, raw json.RawMessage) (json.RawMessage, actions.Reservation, error) {
	if err := authz.ServiceAction(p, c.Clock.Now(), c.Location); err != nil {
		return nil, actions.Reservation{}, err
	}
	params, err := Decode(kind, raw)
	if err != nil {
		return nil, actions.Reservation{}, err
	}
	if kind == "access_create" {
		safe := safety.Prepare(ctx, safety.Local{}, params.Justification)
		if safe.UseForm() {
			return nil, actions.Reservation{}, actions.ErrInvalid
		}
		params.Justification = safe.String()
	}
	cleaned, _ := json.Marshal(params)
	if err := c.Validate(ctx, p, kind, cleaned); err != nil {
		return nil, actions.Reservation{}, err
	}
	reservation := actions.Reservation{Key: "resource:" + params.ResourceID}
	if strings.HasPrefix(kind, "time_off") {
		reservation = actions.Reservation{StartDate: &params.StartDate, EndDate: &params.EndDate}
	}
	if strings.HasSuffix(kind, "_cancel") {
		service := "access"
		if kind == "time_off_cancel" {
			service = "time_off"
		}
		object, e := c.Remote.Get(ctx, p.ID, service, params.RequestID)
		if e != nil {
			return nil, actions.Reservation{}, e
		}
		reservation.Key = "resource:" + object.ResourceID
		if service == "time_off" {
			reservation = actions.Reservation{StartDate: &object.StartDate, EndDate: &object.EndDate}
		}
	}
	return cleaned, reservation, nil
}
func (c Client) Validate(ctx context.Context, p demo.Profile, kind string, raw json.RawMessage) error {
	if err := authz.ServiceAction(p, c.Clock.Now(), c.Location); err != nil {
		return err
	}
	params, err := Decode(kind, raw)
	if err != nil {
		return err
	}
	if kind == "access_create" {
		resources, e := c.Remote.Resources(ctx, p.ID)
		if e != nil {
			return actions.ValidationError{Retryable: true}
		}
		var found *integrations.Resource
		for i := range resources {
			if resources[i].ID == params.ResourceID {
				found = &resources[i]
				break
			}
		}
		if found == nil {
			return authz.ErrNotFound
		}
		if !found.CanRequest {
			return authz.ErrNotAllowed
		}
		if !slices.Contains(found.Levels, params.Level) {
			return integrations.Error{Code: "LEVEL_NOT_ALLOWED", Status: 409}
		}
		if found.RequiresTask && params.Justification == "Для выполнения шагов демонстрационного плана адаптации" {
			return FormError{map[string]string{"justification": "Для этого ресурса опиши свою рабочую задачу; автоматическое обоснование не подходит."}}
		}
		safe := safety.Prepare(ctx, safety.Local{}, params.Justification)
		if safe.UseForm() || safe.String() != params.Justification || utf8.RuneCountInString(params.Justification) < 10 || utf8.RuneCountInString(params.Justification) > 500 {
			return actions.ErrInvalid
		}
		grants, e := c.Remote.Grants(ctx, p.ID)
		if e != nil {
			return actions.ValidationError{Retryable: true}
		}
		for _, g := range grants {
			if g.ResourceID == params.ResourceID {
				return integrations.Error{Code: "ALREADY_GRANTED", Status: 409}
			}
		}
		requests, e := c.Remote.List(ctx, p.ID, "access")
		if e != nil {
			return actions.ValidationError{Retryable: true}
		}
		for _, r := range requests {
			if r.ResourceID == params.ResourceID && (r.Status == "created" || r.Status == "in_review") {
				return integrations.Error{Code: "DUPLICATE_OPEN_REQUEST", Status: 409, ExistingID: r.ID}
			}
		}
	} else if kind == "time_off_create" {
		_, fields := calendar.Validate(c.Clock.Now(), c.Location, params.StartDate, params.EndDate)
		if len(fields) > 0 {
			return FormError{fields}
		}
		if p.ManagerID == nil {
			return authz.ErrNotAllowed
		}
		if c.Authority != nil {
			manager, e := c.Authority.Profile(ctx, *p.ManagerID)
			if e != nil {
				return actions.ValidationError{Retryable: true}
			}
			if !manager.Enabled || manager.Role != "manager" {
				return authz.ErrNotAllowed
			}
		}
		objects, e := c.Remote.List(ctx, p.ID, "time_off")
		if e != nil {
			return actions.ValidationError{Retryable: true}
		}
		for _, r := range objects {
			if r.Status != "cancelled" && r.Status != "rejected" && r.StartDate <= params.EndDate && r.EndDate >= params.StartDate {
				return integrations.Error{Code: "OVERLAPPING_REQUEST", Status: 409, ExistingID: r.ID}
			}
		}
	} else {
		service := "access"
		if kind == "time_off_cancel" {
			service = "time_off"
		}
		object, e := c.Remote.Get(ctx, p.ID, service, params.RequestID)
		if e != nil {
			return e
		}
		if params.Version < 1 || params.Version != object.Version {
			return actions.ErrConflict
		}
		if object.Status == "cancelled" || object.Status == "rejected" || service == "access" && object.Status == "approved" {
			return actions.ErrConflict
		}
		if service == "time_off" && c.Clock.Now().In(c.Location).Format(time.DateOnly) >= object.StartDate {
			return integrations.Error{Code: "CANCELLATION_TOO_LATE", Status: 409}
		}
	}
	return nil
}
func (c Client) Execute(ctx context.Context, job actions.Work) actions.Result {
	if !actions.ValidID(job.Attempt.ID) || !actions.ValidID(job.Attempt.CardID) || !actions.ValidID(job.Attempt.OperationKey) || job.Attempt.CardVersion < 1 || job.Attempt.UserID == "" || job.Attempt.State != "queued" {
		return actions.Result{Kind: actions.Rejected, Code: "NOT_ALLOWED", Source: "local_validation"}
	}
	if _, e := Decode(job.CardType, job.Attempt.Parameters); e != nil {
		return actions.Result{Kind: actions.Rejected, Code: "VALIDATION_ERROR", Source: "local_validation"}
	}
	if c.Authority == nil {
		return actions.Result{Kind: actions.Rejected, Code: "NOT_ALLOWED", Source: "local_validation"}
	}
	actual, e := c.Authority.Attempt(ctx, job.Attempt.UserID, job.Attempt.ID)
	if e != nil || actual.CardID != job.Attempt.CardID || actual.CardVersion != job.Attempt.CardVersion || actual.OperationKey != job.Attempt.OperationKey || actual.State != "sending" || !actual.MayHaveSent || !bytes.Equal(actual.Parameters, job.Attempt.Parameters) {
		return actions.Result{Kind: actions.Rejected, Code: "NOT_ALLOWED", Source: "local_validation"}
	}
	return c.Remote.Execute(ctx, job)
}
func (c Client) Reconcile(ctx context.Context, job actions.Work) (actions.Reconciliation, error) {
	if !actions.ValidID(job.Attempt.OperationKey) {
		return actions.Reconciliation{}, errors.New("NOT_ALLOWED")
	}
	return c.Remote.Reconcile(ctx, job)
}

func (c Client) Read(ctx context.Context, p demo.Profile, name string, raw json.RawMessage) (any, error) {
	if c.Authority == nil {
		return nil, authz.ErrNotAllowed
	}
	current, e := c.Authority.Profile(ctx, p.ID)
	if e != nil || !authz.SameScope(p, current) {
		return nil, authz.ErrNotAllowed
	}
	var input struct {
		Query     string `json:"query"`
		RequestID string `json:"request_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 8192 || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, actions.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, actions.ErrInvalid
	}
	switch name {
	case "onboarding.get_plan":
		if len(fields) != 0 || c.Source == nil {
			return nil, actions.ErrInvalid
		}
		steps, e := c.Source.Steps(ctx, p.ID)
		if e != nil {
			return nil, e
		}
		return onboarding.Project(p, steps, c.Clock.Now(), c.Location), nil
	case "access.list_resources":
		for key := range fields {
			if key != "query" {
				return nil, actions.ErrInvalid
			}
		}
		items, e := c.Remote.Resources(ctx, p.ID)
		if e != nil {
			return nil, e
		}
		filtered := []integrations.Resource{}
		q := strings.ToLower(input.Query)
		for _, item := range items {
			if q == "" || strings.Contains(strings.ToLower(item.Name+" "+item.ID+" "+strings.Join(item.Aliases, " ")), q) {
				filtered = append(filtered, item)
			}
		}
		return map[string]any{"items": filtered}, nil
	case "access.get_status", "time_off.get_status":
		if len(fields) != 1 || input.RequestID == "" {
			return nil, actions.ErrInvalid
		}
		service := "access"
		if name == "time_off.get_status" {
			service = "time_off"
		}
		return c.Remote.Get(ctx, p.ID, service, input.RequestID)
	case "documents.search":
		if len(fields) != 1 || fields["query"] == nil || c.Source == nil || c.Directory == nil {
			return nil, actions.ErrInvalid
		}
		query := safety.Prepare(ctx, safety.Local{}, input.Query)
		if query.UseForm() {
			return nil, actions.ErrInvalid
		}
		frame, e := (agent.Search{Builder: agent.Builder{Source: c.Source, Directory: c.Directory}}).Find(ctx, p.ID, query)
		if e != nil {
			return nil, e
		}
		return map[string]any{"items": frame.Documents}, nil
	default:
		return nil, actions.ErrInvalid
	}
}
