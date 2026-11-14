package tools

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestClosedSchemasAndConfirmationContext(t *testing.T) {
	for _, s := range Schemas() {
		if !json.Valid(s.Input) || !json.Valid(s.Output) {
			t.Fatal(s)
		}
	}
	for _, tc := range []struct{ kind, body string }{{"unknown", `{}`}, {"access_create", `{"user_id":"maxim"}`}, {"time_off_create", `{"start_date":"2026-11-13","end_date":"2026-11-13","reason":"private"}`}, {"access_cancel", `{"request_id":"ACC-1","expected_version":null}`}, {"access_create", `null`}, {"access_create", `{} {}`}} {
		if _, e := Decode(tc.kind, json.RawMessage(tc.body)); e == nil {
			t.Fatal("invalid schema accepted")
		}
	}
	c := Client{Clock: clock.Fixed{Instant: time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)}, Location: time.UTC}
	ctx := context.Background()
	p := demo.Profiles()[2]
	if _, _, e := c.Prepare(ctx, p, "access_create", json.RawMessage(`{}`)); e == nil {
		t.Fatal("buddy permission bypass")
	}
	for _, job := range []actions.Work{{}, {CardType: "arbitrary.tool", Attempt: actions.Attempt{ID: actions.ID(), CardID: actions.ID(), CardVersion: 1, UserID: "alina", OperationKey: actions.ID(), State: "queued", Parameters: json.RawMessage(`{}`)}}, {CardType: "access_create", Attempt: actions.Attempt{ID: actions.ID(), CardID: actions.ID(), CardVersion: 1, UserID: "alina", OperationKey: actions.ID(), State: "queued", Parameters: json.RawMessage(`{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки"}`)}}} {
		if result := c.Execute(ctx, job); result.Kind != actions.Rejected {
			t.Fatal("unconfirmed context executed", result)
		}
	}
	if _, e := c.Reconcile(ctx, actions.Work{}); e == nil {
		t.Fatal("invalid key accepted")
	}
}
