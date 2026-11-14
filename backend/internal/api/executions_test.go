package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/execution"
	"ai-buddy/backend/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExecutionHTTPContract(t *testing.T) {
	store, _, _, h := businessFixture(t)
	origin := "http://localhost:5173"
	if got := request(h, "GET", "/api/executions", "", origin, nil); got.Code != 401 {
		t.Fatal(got.Code)
	}
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	ctx := context.Background()
	repo := storage.ExecutionRepository{Store: store}
	now := store.OperationalClock.Now()
	id := actions.ID()
	if e := repo.Create(ctx, execution.Run{ID: id, UserID: "alina", Scenario: "orientation", State: "queued", StartedAt: now, DetailComplete: true}); e != nil {
		t.Fatal(e)
	}
	for i, kind := range []string{"context", "answer"} {
		f := execution.Fact{Key: kind, Node: execution.Node{ID: kind, Kind: kind, LabelCode: kind, State: "succeeded"}, State: "completed", At: now}
		if i == 0 {
			f.State = "running"
		}
		if e := repo.Append(ctx, id, f); e != nil {
			t.Fatal(e)
		}
	}
	first := request(h, "GET", "/api/executions/"+id+"?limit=1&after_sequence=0", "", origin, cookie)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var page execution.Page
	if e := json.Unmarshal(first.Body.Bytes(), &page); e != nil {
		t.Fatal(e)
	}
	if page.NextSequence != 1 || page.Snapshot.LastSequence != 2 || !page.HasMore {
		t.Fatal(page)
	}
	second := request(h, "GET", "/api/executions/"+id+"?limit=1&after_sequence=1", "", origin, cookie)
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	if e := json.Unmarshal(second.Body.Bytes(), &page); e != nil {
		t.Fatal(e)
	}
	if page.NextSequence != 2 || page.HasMore {
		t.Fatal(page)
	}
	for _, path := range []string{"/api/executions?limit=0", "/api/executions?limit=101", "/api/executions?limit=", "/api/executions?limit=1&limit=2", "/api/executions?cursor=", "/api/executions?cursor=bad", "/api/executions?active=1", "/api/executions?x=1", "/api/executions/" + id + "?after_sequence=-1", "/api/executions/" + id + "?after_sequence=", "/api/executions/" + id + "?after_sequence=x", "/api/executions/" + id + "?limit=0", "/api/executions/" + id + "?x=1"} {
		got := request(h, "GET", path, "", origin, cookie)
		if got.Code != 400 {
			t.Fatal(path, got.Code, got.Body.String())
		}
	}
	for _, path := range []string{"/api/executions?limit=1", "/api/executions?active=true", "/api/executions?active=false"} {
		if got := request(h, "GET", path, "", origin, cookie); got.Code != 200 {
			t.Fatal(path, got.Code)
		}
	}
	other := request(h, "POST", "/api/demo/session", `{"user_id":"maxim"}`, origin, nil).Result().Cookies()[0]
	for _, target := range []string{id, actions.ID(), "bad"} {
		got := request(h, "GET", "/api/executions/"+target, "", origin, other)
		if got.Code != 404 {
			t.Fatal(got.Code)
		}
	}
}

func TestExecutionFactsFollowCardAndSavedResult(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	// Button processing has no model node; replay retains the accepted route.
	key := actions.ID()
	body := `{"client_message_id":"` + key + `","intent":"next_step","slots":{}}`
	accepted := request(h, "POST", "/api/dialogue/actions", body, origin, cookie)
	var queued struct {
		JobID string   `json:"job_id"`
		IDs   []string `json:"execution_ids"`
	}
	if e := json.Unmarshal(accepted.Body.Bytes(), &queued); e != nil || len(queued.IDs) != 1 {
		t.Fatal(accepted.Body.String(), e)
	}
	replay := request(h, "POST", "/api/dialogue/actions", body, origin, cookie)
	var duplicate struct {
		IDs []string `json:"execution_ids"`
	}
	_ = json.Unmarshal(replay.Body.Bytes(), &duplicate)
	if len(duplicate.IDs) != 1 || duplicate.IDs[0] != queued.IDs[0] {
		t.Fatal("lost stable correlation")
	}
	conflict := request(h, "POST", "/api/dialogue/actions", `{"client_message_id":"`+key+`","intent":"access_request","slots":{}}`, origin, cookie)
	if conflict.Code != 409 {
		t.Fatal(conflict.Code)
	}
	finishMessage(t, b, queued.JobID)
	repo := storage.ExecutionRepository{Store: store}
	p, e := repo.Read(ctx, "alina", queued.IDs[0], 0, 100)
	if e != nil || p.Snapshot.State != "completed" {
		t.Fatal(p, e)
	}
	for _, n := range p.Snapshot.Nodes {
		if n.Kind == "model" || n.Kind == "service" {
			t.Fatal("invented call", n)
		}
	}
	// A direct form uses a fresh route and cannot send before confirmation.
	cardReply := request(h, "POST", "/api/cards", `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки"}}`, origin, cookie)
	var prepared struct {
		Card actions.Card `json:"card"`
	}
	_ = json.Unmarshal(cardReply.Body.Bytes(), &prepared)
	c := prepared.Card
	if len(c.ExecutionIDs) != 1 {
		t.Fatal("direct form response lost its execution", prepared)
	}
	c, e = store.Card(ctx, "alina", c.ID, b.Tools.Clock.Now())
	if e != nil || len(c.ExecutionIDs) != 1 {
		t.Fatal(c, e)
	}
	id := c.ExecutionIDs[0]
	p, e = repo.Read(ctx, "alina", id, 0, 100)
	if e != nil || p.Snapshot.State != "waiting_user" || remote.count != 0 {
		t.Fatal(p, e, remote.count)
	}
	stale := request(h, "POST", "/api/cards/"+c.ID+"/confirm", `{"expected_version":2}`, origin, cookie)
	if stale.Code != 409 || remote.count != 0 {
		t.Fatal(stale.Code, remote.count)
	}
	sent := request(h, "POST", "/api/cards/"+c.ID+"/confirm", `{"expected_version":1}`, origin, cookie)
	if sent.Code != 202 {
		t.Fatal(sent.Body.String())
	}
	again := request(h, "POST", "/api/cards/"+c.ID+"/confirm", `{"expected_version":1}`, origin, cookie)
	if again.Code != 202 {
		t.Fatal(again.Code)
	}
	// Failure to commit the mandatory final fact rolls the result back and retains unknown protection.
	_, e = store.Pool.Exec(ctx, `CREATE FUNCTION reject_route_final() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='persistence' THEN RAISE EXCEPTION 'synthetic journal failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_route_final BEFORE INSERT ON execution_events FOR EACH ROW EXECUTE FUNCTION reject_route_final()`)
	if e != nil {
		t.Fatal(e)
	}
	worker := actions.Worker{Store: store, Client: b.Tools, BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Timeout: time.Second, Lease: 2 * time.Second, Delays: []time.Duration{time.Second, 2 * time.Second}}
	if e = worker.Once(ctx, b.Tools.Clock.Now()); e != nil {
		t.Fatal(e)
	}
	p, e = repo.Read(ctx, "alina", id, 0, 100)
	if e != nil || p.Snapshot.State != "reconciling" || remote.count != 1 {
		t.Fatal(p, e, remote.count)
	}
	var objects int
	if e = store.Pool.QueryRow(ctx, `SELECT count(*) FROM service_objects`).Scan(&objects); e != nil || objects != 0 {
		t.Fatal("uncommitted result exposed", objects, e)
	}
	if _, e = store.Pool.Exec(ctx, `DROP TRIGGER reject_route_final ON execution_events; DROP FUNCTION reject_route_final()`); e != nil {
		t.Fatal(e)
	}
	if e = worker.Once(ctx, b.Tools.Clock.Now().Add(2*time.Second)); e != nil {
		t.Fatal(e)
	}
	p, e = repo.Read(ctx, "alina", id, 0, 100)
	if e != nil || p.Snapshot.State != "completed" || p.Snapshot.ResultCode != "created" || remote.count != 1 {
		t.Fatal(p, e, remote.count)
	}
	for _, n := range p.Snapshot.Nodes {
		if n.Kind == "service" && n.State != "succeeded" {
			t.Fatal("stale service wait", n)
		}
	}
}

func TestExecutionClarificationTopicAndLease(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "нужен доступ в PAY")
	raw, e := store.Dialogue(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	var first DialogueState
	_ = json.Unmarshal(raw, &first)
	if first.ExecutionID == "" {
		t.Fatal("missing logical route")
	}
	postText(t, b, h, cookie, "чтение")
	raw, e = store.Dialogue(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	var continued DialogueState
	_ = json.Unmarshal(raw, &continued)
	if continued.ExecutionID != first.ExecutionID {
		t.Fatal("clarification detached", first, continued)
	}
	postText(t, b, h, cookie, "Как оформить отгул?")
	raw, e = store.Dialogue(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	var changed DialogueState
	_ = json.Unmarshal(raw, &changed)
	if changed.ExecutionID == first.ExecutionID {
		t.Fatal("topic changed original request")
	}
	p, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", first.ExecutionID, 0, 100)
	if e != nil || p.Snapshot.State == "completed" {
		t.Fatal("unfinished action completed by another question", p, e)
	}
	// Only the current worker may write intermediate facts.
	m, _, e := store.AcceptAction(ctx, "alina", actions.ID(), "next_step", actions.Slots{}, b.Tools.Clock.Now())
	if e != nil {
		t.Fatal(e)
	}
	work, e := store.ClaimMessage(ctx, b.Tools.Clock.Now(), time.Second)
	if e != nil || work.Message.ID != m.ID {
		t.Fatal(work, e)
	}
	replacement, e := store.ClaimMessage(ctx, b.Tools.Clock.Now().Add(2*time.Second), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.ObserveMessage(ctx, work, "context", "succeeded"); e != actions.ErrLeaseLost {
		t.Fatal("lost lease wrote fact", e)
	}
	if e = store.ObserveMessage(ctx, replacement, "context", "succeeded"); e != nil {
		t.Fatal(e)
	}
}

type executionTimeoutProvider struct{ agent.Provider }

func (p executionTimeoutProvider) Recognize(ctx context.Context, _ agent.Context, _ string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestExecutionTimeoutAndTelemetryDegradation(t *testing.T) {
	for _, scenario := range []string{"timeout", "telemetry"} {
		t.Run(scenario, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			ctx := context.Background()
			origin := "http://localhost:5173"
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
			if scenario == "timeout" {
				b.Provider = executionTimeoutProvider{agent.Fake{}}
				b.LLMTimeout = 5 * time.Millisecond
			} else {
				if _, e := store.Pool.Exec(ctx, `CREATE FUNCTION reject_intermediate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='context' THEN RAISE EXCEPTION 'synthetic telemetry failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_intermediate BEFORE INSERT ON execution_events FOR EACH ROW EXECUTE FUNCTION reject_intermediate()`); e != nil {
					t.Fatal(e)
				}
			}
			job := postText(t, b, h, cookie, "Как оформить отгул?")
			ids, e := store.ExecutionIDs(ctx, "alina", "job", job)
			if e != nil || len(ids) != 1 {
				t.Fatal(ids, e)
			}
			page, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", ids[0], 0, 100)
			if e != nil || page.Snapshot.State != "completed" || remote.count != 0 {
				t.Fatal(page, e, remote.count)
			}
			if scenario == "timeout" {
				if page.Snapshot.ResultCode != "fallback" {
					t.Fatal("timeout did not preserve fallback", page)
				}
				found := false
				for _, n := range page.Snapshot.Nodes {
					if n.Kind == "model" && n.State == "failed" {
						found = true
					}
					if n.Kind == "service" {
						t.Fatal("invented service call")
					}
				}
				if !found {
					t.Fatal("timeout absent from journal")
				}
			} else {
				if page.DetailComplete {
					t.Fatal("telemetry gap hidden")
				}
				for _, n := range page.Snapshot.Nodes {
					if n.Kind == "context" {
						t.Fatal("reconstructed missing fact")
					}
				}
			}
		})
	}
}
func TestExecutionReplayPreservesUnsentCard(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	payload := `{"client_message_id":"` + actions.ID() + `","intent":"time_off_request","slots":{"start_date":"2026-11-20","end_date":"2026-11-20"}}`
	accepted := request(h, "POST", "/api/dialogue/actions", payload, origin, cookie)
	var q struct {
		Job string   `json:"job_id"`
		IDs []string `json:"execution_ids"`
	}
	if e := json.Unmarshal(accepted.Body.Bytes(), &q); e != nil || len(q.IDs) != 1 {
		t.Fatal(accepted.Body.String(), e)
	}
	if _, e := store.Pool.Exec(ctx, `CREATE FUNCTION reject_assistant_reply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.role='assistant' THEN RAISE EXCEPTION 'synthetic reply persistence failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_assistant_reply BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION reject_assistant_reply()`); e != nil {
		t.Fatal(e)
	}
	if e := b.ProcessOnce(ctx, store.OperationalClock.Now()); e == nil {
		t.Fatal("reply failure not exercised")
	}
	if _, e := store.Pool.Exec(ctx, `DROP TRIGGER reject_assistant_reply ON messages; DROP FUNCTION reject_assistant_reply()`); e != nil {
		t.Fatal(e)
	}
	if e := b.ProcessOnce(ctx, store.OperationalClock.Now().Add(20*time.Second)); e != nil {
		t.Fatal(e)
	}
	page, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", q.IDs[0], 0, 100)
	if e != nil || page.Snapshot.State != "waiting_user" || page.Snapshot.CurrentNodeID != "confirmation" || remote.count != 0 {
		t.Fatal("replayed draft claimed completion", page, e, remote.count)
	}
	var count int
	if e = store.Pool.QueryRow(ctx, `SELECT count(*) FROM cards`).Scan(&count); e != nil || count != 1 {
		t.Fatal("replay duplicated card", count, e)
	}
}

type executionMultiProvider struct{ agent.Provider }

func (p executionMultiProvider) Recognize(context.Context, agent.Context, string) ([]byte, error) {
	return []byte(`{"intents":[{"name":"access_request","slots":{"resource_id":"space-payments","level":"read","justification":"Для демонстрационной рабочей задачи"},"confidence":1},{"name":"time_off_request","slots":{"start_date":"2026-11-20","end_date":"2026-11-20"},"confidence":1}],"ambiguities":[]}`), nil
}
func TestExecutionMultipleIntentIsolation(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	b.Provider = executionMultiProvider{agent.Fake{}}
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужны доступ и отгул")
	raw, e := store.Dialogue(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	var state DialogueState
	_ = json.Unmarshal(raw, &state)
	if len(state.PendingExecutions) != 1 || state.PendingExecutions[0] == state.ExecutionID || len(state.Pending) != 1 {
		t.Fatal("intent routes were combined", state)
	}
	repo := storage.ExecutionRepository{Store: store}
	first, e := repo.Read(ctx, "alina", state.ExecutionID, 0, 100)
	if e != nil || first.Snapshot.State != "waiting_user" || first.Snapshot.Scenario != "access" {
		t.Fatal(first, e)
	}
	second, e := repo.Read(ctx, "alina", state.PendingExecutions[0], 0, 100)
	if e != nil || second.Snapshot.State != "waiting_user" || second.Snapshot.Scenario != "time_off" {
		t.Fatal(second, e)
	}
	for _, n := range second.Snapshot.Nodes {
		if n.Kind == "service" || n.Kind == "model" {
			t.Fatal("unstarted second intent depicted as work", n)
		}
	}
	if remote.count != 0 {
		t.Fatal("confirmation inherited between intents")
	}
}
func TestExecutionModelCallsAndSafeDenial(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	job := postText(t, b, h, cookie, "Как оформить отгул?")
	ids, e := store.ExecutionIDs(ctx, "alina", "job", job)
	if e != nil || len(ids) != 1 {
		t.Fatal(ids, e)
	}
	page, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", ids[0], 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	for _, ev := range page.Events {
		if ev.Node.Kind == "model" && ev.Node.State == "succeeded" {
			calls++
		}
		if ev.Node.Kind == "service" {
			t.Fatal("instruction mutated service")
		}
	}
	if calls != 2 {
		t.Fatal("recognition and fragment selection not separately observed", calls)
	}
	raw := `{"client_message_id":"` + actions.ID() + `","intent":"access_request","slots":{"resource_id":"salary-reports","level":"read","justification":"Для демонстрационной рабочей задачи"}}`
	response := request(h, "POST", "/api/dialogue/actions", raw, origin, cookie)
	var queued struct {
		Job string   `json:"job_id"`
		IDs []string `json:"execution_ids"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &queued)
	finishMessage(t, b, queued.Job)
	page, e = (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", queued.IDs[0], 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(page)
	for _, word := range []string{"salary", "justification", "prompt", "secret"} {
		if strings.Contains(string(encoded), word) {
			t.Fatal("denial disclosed hidden data", word)
		}
	}
	if remote.count != 0 {
		t.Fatal("denial sent mutation")
	}
}

type executionRejectCancel struct{ delegate http.RoundTripper }

func (r executionRejectCancel) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/cancel") {
		return reply(409, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR"}}), nil
	}
	return r.delegate.RoundTrip(req)
}
func TestExecutionTransferSecondOutcome(t *testing.T) {
	for _, outcome := range []string{"rejected", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			ctx := context.Background()
			origin := "http://localhost:5173"
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
			now := b.Tools.Clock.Now()
			remote.objects["ABS-9000"] = actions.Object{ID: "ABS-9000", Service: "time_off", UserID: "alina", StartDate: "2026-11-20", EndDate: "2026-11-20", Workdays: 1, ApproverID: "olga", Status: "submitted", Version: 1, CreatedAt: now, UpdatedAt: now}
			payload := `{"client_message_id":"` + actions.ID() + `","intent":"time_off_request","slots":{"request_id":"ABS-9000","start_date":"2026-11-23","end_date":"2026-11-23"}}`
			accepted := request(h, "POST", "/api/dialogue/actions", payload, origin, cookie)
			var q struct {
				Job string   `json:"job_id"`
				IDs []string `json:"execution_ids"`
			}
			_ = json.Unmarshal(accepted.Body.Bytes(), &q)
			finishMessage(t, b, q.Job)
			worker := actions.Worker{Store: store, Client: b.Tools, BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 3 * time.Second}}
			confirm := func() {
				t.Helper()
				cards, e := store.Cards(ctx, "alina", now)
				if e != nil || len(cards) != 1 {
					t.Fatal(cards, e)
				}
				response := request(h, "POST", "/api/cards/"+cards[0].ID+"/confirm", fmt.Sprintf(`{"expected_version":%d}`, cards[0].Version), origin, cookie)
				if response.Code != 202 {
					t.Fatal(response.Body.String())
				}
			}
			confirm()
			if e := worker.Once(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			if e := b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			postText(t, b, h, cookie, "да")
			if outcome == "rejected" {
				b.Tools.Remote.HTTP = &http.Client{Transport: executionRejectCancel{remote}}
			} else {
				remote.unknown = true
			}
			worker.Client = b.Tools
			confirm()
			if e := worker.Once(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			page, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", q.IDs[0], 0, 100)
			if e != nil || len(page.Snapshot.Children) != 2 || page.Snapshot.Children[0].State != "completed" || page.Snapshot.State == "completed" {
				t.Fatal(page, e)
			}
			if outcome == "rejected" {
				if page.Snapshot.State != "rejected" || page.Snapshot.ResultCode != "partial" || remote.objects["ABS-9000"].Status != "submitted" {
					t.Fatal(page)
				}
			} else if page.Snapshot.State != "reconciling" || page.Snapshot.Children[1].State != "reconciling" {
				t.Fatal(page)
			}
		})
	}
}
