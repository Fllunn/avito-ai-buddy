// Package integrations speaks only the documented synthetic service API.
package integrations

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/demo"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("SERVICE_UNAVAILABLE")

type Error struct {
	Code       string
	Status     int
	ExistingID string
}

func (e Error) Error() string { return e.Code }

type Client struct {
	BaseURL, Secret string
	HTTP            *http.Client
	Timeout         time.Duration
}
type Resource struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases"`
	CanRequest   bool     `json:"can_request"`
	Levels       []string `json:"levels"`
	DefaultLevel *string  `json:"default_level"`
	Teams        []string `json:"recommended_team_ids"`
	RequiresTask bool     `json:"requires_task"`
	ApproverID   *string  `json:"approver_id"`
}
type Grant struct {
	ResourceID      string    `json:"resource_id"`
	Level           string    `json:"level"`
	GrantedAt       time.Time `json:"granted_at"`
	SourceRequestID string    `json:"source_request_id"`
}

func (c Client) call(ctx context.Context, user, method, path, service string, input, output any) error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return actions.ErrInvalid
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if e != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	req.Header.Set("X-Actor-ID", user)
	req.Header.Set("X-Service", service)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := copy.Do(req)
	if e != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return ErrUnavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Existing string `json:"existing_id"`
		}
		if json.Unmarshal(raw, &problem) != nil {
			return Error{"SERVICE_ERROR", res.StatusCode, ""}
		}
		allowed := map[string]bool{"VALIDATION_ERROR": true, "UNAUTHENTICATED": true, "NOT_ALLOWED": true, "NOT_FOUND": true, "DUPLICATE_OPEN_REQUEST": true, "ALREADY_GRANTED": true, "LEVEL_NOT_ALLOWED": true, "INVALID_STATE": true, "VERSION_CONFLICT": true, "IDEMPOTENCY_CONFLICT": true, "OPERATION_CLOSED": true, "OVERLAPPING_REQUEST": true, "CANCELLATION_TOO_LATE": true, "SERVICE_UNAVAILABLE": true}
		if !allowed[problem.Error.Code] {
			problem.Error.Code = "SERVICE_ERROR"
		}
		if !strings.HasPrefix(problem.Existing, "ACC-") && !strings.HasPrefix(problem.Existing, "ABS-") {
			problem.Existing = ""
		}
		return Error{problem.Error.Code, res.StatusCode, problem.Existing}
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return actions.ErrContract
	}
	return nil
}
func (c Client) Resources(ctx context.Context, user string) ([]Resource, error) {
	var data struct {
		Items []Resource `json:"items"`
	}
	e := c.call(ctx, user, "GET", "/v1/resources", "access", nil, &data)
	return data.Items, e
}
func (c Client) Directory(ctx context.Context, p demo.Profile) ([]demo.Resource, error) {
	items, e := c.Resources(ctx, p.ID)
	if e != nil {
		return nil, e
	}
	result := []demo.Resource{}
	for _, r := range items {
		kind := "recommended"
		if !r.CanRequest {
			kind = "closed"
		}
		if r.RequiresTask {
			kind = "task"
		}
		owner, level := "", ""
		if r.ApproverID != nil {
			owner = *r.ApproverID
		}
		if r.DefaultLevel != nil {
			level = *r.DefaultLevel
		}
		result = append(result, demo.Resource{ID: r.ID, Name: r.Name, Kind: kind, Levels: r.Levels, DefaultLevel: level, ApproverID: owner, RecommendedTeams: r.Teams, Synonyms: r.Aliases})
	}
	return result, nil
}
func (c Client) Grants(ctx context.Context, user string) ([]Grant, error) {
	var data struct {
		Items []Grant `json:"items"`
	}
	e := c.call(ctx, user, "GET", "/v1/grants", "access", nil, &data)
	return data.Items, e
}
func path(service string) string {
	if service == "time_off" {
		return "/v1/time-off-requests"
	}
	return "/v1/access-requests"
}
func (c Client) List(ctx context.Context, user, service string) ([]actions.Object, error) {
	var data struct {
		Items []actions.Object `json:"items"`
	}
	e := c.call(ctx, user, "GET", path(service), service, nil, &data)
	for i := range data.Items {
		data.Items[i].Service = service
		if data.Items[i].UserID != user {
			return nil, actions.ErrContract
		}
	}
	return data.Items, e
}
func (c Client) Get(ctx context.Context, user, service, id string) (actions.Object, error) {
	var object actions.Object
	e := c.call(ctx, user, "GET", path(service)+"/"+url.PathEscape(id), service, nil, &object)
	object.Service = service
	if e == nil && object.UserID != user {
		return actions.Object{}, actions.ErrContract
	}
	return object, e
}
func (c Client) Execute(ctx context.Context, job actions.Work) actions.Result {
	service := "access"
	if strings.HasPrefix(job.CardType, "time_off") {
		service = "time_off"
	}
	var input map[string]any
	if json.Unmarshal(job.Attempt.Parameters, &input) != nil {
		return actions.Result{Kind: actions.Rejected, Code: "VALIDATION_ERROR", Source: "local_validation"}
	}
	endpoint := path(service)
	if strings.HasSuffix(job.CardType, "_cancel") {
		id, _ := input["request_id"].(string)
		endpoint += "/" + url.PathEscape(id) + "/cancel"
		input = map[string]any{"expected_version": input["expected_version"]}
	}
	input["operation_key"] = job.Attempt.OperationKey
	var object actions.Object
	e := c.call(ctx, job.Attempt.UserID, "POST", endpoint, service, input, &object)
	if e == nil {
		object.Service = service
		if object.UserID != job.Attempt.UserID {
			return actions.Result{Kind: actions.Unknown, Source: "service"}
		}
		return actions.Result{Kind: actions.Success, Object: &object, Source: "service"}
	}
	var problem Error
	if errors.As(e, &problem) {
		if problem.Status == 503 && problem.Code == "SERVICE_UNAVAILABLE" {
			return actions.Result{Kind: actions.Unavailable, Code: problem.Code, Source: "service"}
		}
		if problem.Status >= 400 && problem.Status < 500 {
			return actions.Result{Kind: actions.Rejected, Code: problem.Code, Source: "service"}
		}
	}
	return actions.Result{Kind: actions.Unknown, Source: "service"}
}
func (c Client) Reconcile(ctx context.Context, job actions.Work) (actions.Reconciliation, error) {
	service := "access"
	if strings.HasPrefix(job.CardType, "time_off") {
		service = "time_off"
	}
	var data struct {
		State  string `json:"state"`
		Key    string `json:"operation_key"`
		Result struct {
			Kind actions.Outcome `json:"kind"`
			HTTP int             `json:"http_status"`
			Body json.RawMessage `json:"body"`
		} `json:"result"`
	}
	e := c.call(ctx, job.Attempt.UserID, "POST", "/v1/operations/"+url.PathEscape(job.Attempt.OperationKey)+"/reconcile", service, struct{}{}, &data)
	if e != nil {
		return actions.Reconciliation{}, e
	}
	if data.Key != job.Attempt.OperationKey {
		return actions.Reconciliation{}, actions.ErrContract
	}
	result := actions.Reconciliation{State: data.State, Result: actions.Result{Kind: data.Result.Kind, Source: "service"}}
	if data.State == "completed" {
		if data.Result.Kind == actions.Success {
			var object actions.Object
			if json.Unmarshal(data.Result.Body, &object) != nil || object.UserID != job.Attempt.UserID {
				return result, actions.ErrContract
			}
			object.Service = service
			result.Result.Object = &object
		} else {
			var problem struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(data.Result.Body, &problem) != nil {
				return result, actions.ErrContract
			}
			result.Result.Code = problem.Error.Code
		}
	}
	return result, nil
}
