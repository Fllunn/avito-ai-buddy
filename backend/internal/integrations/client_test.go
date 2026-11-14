package integrations

import (
	"ai-buddy/backend/internal/actions"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(status int, body string) Client {
	return Client{BaseURL: "http://synthetic.test", Secret: "synthetic", HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}
}
func work() actions.Work {
	return actions.Work{CardType: "access_create", Attempt: actions.Attempt{ID: actions.ID(), UserID: "alina", OperationKey: actions.ID(), Parameters: json.RawMessage(`{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки"}`)}}
}
func TestTypedOutcomesAndErrorMinimization(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status int
		body   string
		kind   actions.Outcome
	}{{503, `{"error":{"code":"SERVICE_UNAVAILABLE"}}`, actions.Unavailable}, {500, `{"error":{"code":"SERVICE_ERROR"}}`, actions.Unknown}, {401, `{"error":{"code":"UNAUTHENTICATED"}}`, actions.Rejected}, {409, `{"error":{"code":"DUPLICATE_OPEN_REQUEST"},"existing_id":"ACC-1"}`, actions.Rejected}, {400, `{"error":{"code":"raw-secret-value"}}`, actions.Rejected}, {200, `bad-json`, actions.Unknown}, {200, `{"user_id":"maxim"}`, actions.Unknown}} {
		c := fixture(tc.status, tc.body)
		r := c.Execute(ctx, work())
		if r.Kind != tc.kind || strings.Contains(r.Code, "raw-secret") {
			t.Fatal(r)
		}
	}
	c := fixture(200, `{"id":"ACC-1","user_id":"alina"}`)
	if r := c.Execute(ctx, work()); r.Kind != actions.Success || r.Object.Service != "access" {
		t.Fatal(r)
	}
	job := work()
	job.CardType = "time_off_cancel"
	job.Attempt.Parameters = json.RawMessage(`{"request_id":"ABS-1","expected_version":2}`)
	c = fixture(200, `{"id":"ABS-1","user_id":"alina"}`)
	if r := c.Execute(ctx, job); r.Kind != actions.Success || r.Object.Service != "time_off" {
		t.Fatal(r)
	}
	job.Attempt.Parameters = json.RawMessage(`bad`)
	if r := c.Execute(ctx, job); r.Source != "local_validation" {
		t.Fatal(r)
	}
	c = Client{BaseURL: "http://synthetic.test", HTTP: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, errors.New("raw-private") })}}
	if r := c.Execute(ctx, work()); r.Kind != actions.Unknown {
		t.Fatal(r)
	}
	c = fixture(500, strings.Repeat("x", (1<<20)+1))
	if r := c.Execute(ctx, work()); r.Kind != actions.Unknown {
		t.Fatal(r)
	}
}
func TestReconciliationContract(t *testing.T) {
	job := work()
	ctx := context.Background()
	for _, state := range []string{"pending", "not_created", "completed"} {
		body := map[string]any{"state": state, "operation_key": job.Attempt.OperationKey, "result": map[string]any{"kind": "success", "http_status": 201, "body": map[string]string{"id": "ACC-1", "user_id": "alina"}}}
		raw, _ := json.Marshal(body)
		answer, e := fixture(200, string(raw)).Reconcile(ctx, job)
		if e != nil || answer.State != state {
			t.Fatal(answer, e)
		}
	}
	raw := `{"state":"completed","operation_key":"` + job.Attempt.OperationKey + `","result":{"kind":"rejected","http_status":409,"body":{"error":{"code":"INVALID_STATE"}}}}`
	answer, e := fixture(200, raw).Reconcile(ctx, job)
	if e != nil || answer.Result.Code != "INVALID_STATE" {
		t.Fatal(answer, e)
	}
	if _, e := fixture(200, `{"state":"not_created","operation_key":"wrong"}`).Reconcile(ctx, job); e != actions.ErrContract {
		t.Fatal(e)
	}
	if _, e := fixture(503, `{"error":{"code":"SERVICE_UNAVAILABLE"}}`).Reconcile(ctx, job); e == nil {
		t.Fatal("outage accepted")
	}
	if (Error{Code: "NOT_ALLOWED"}).Error() != "NOT_ALLOWED" {
		t.Fatal("wrong error")
	}
	c := fixture(200, `{"items":[{"id":"ACC-1","user_id":"maxim"}]}`)
	if _, e := c.List(ctx, "alina", "access"); e != actions.ErrContract {
		t.Fatal("foreign list accepted")
	}
	c.Timeout = time.Millisecond
}
