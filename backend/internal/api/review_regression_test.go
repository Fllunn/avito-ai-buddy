package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type astraConfirmProvider struct{ agent.Fake }

func (astraConfirmProvider) Recognize(context.Context, agent.Context, string) ([]byte, error) {
	return []byte(`{"intents":[{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`), nil
}
func TestAstraModelCannotAuthorize(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в monitoring")
	cards, e := store.Cards(context.Background(), "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 1 {
		t.Fatal(cards, e)
	}
	b.Provider = astraConfirmProvider{}
	postText(t, b, h, cookie, "Пока ничего не отправляй")
	worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second}}
	e = worker.Once(context.Background(), store.OperationalClock.Now())
	t.Logf("worker=%v external_mutations=%d", e, remote.count)
	if remote.count != 0 {
		t.Fatal("model output authorized mutation despite explicit refusal")
	}
}
func TestAstraJobRechecksDocumentAccess(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	job := postText(t, b, h, cookie, "Как у нас пишут код?")
	before := request(h, "GET", "/api/jobs/"+job, "", "", cookie)
	var parsed struct {
		Result struct {
			Items []struct {
				Answer *agent.Answer `json:"answer"`
			} `json:"items"`
		} `json:"result"`
	}
	if e := json.Unmarshal(before.Body.Bytes(), &parsed); e != nil {
		t.Fatal(e)
	}
	if len(parsed.Result.Items) == 0 || parsed.Result.Items[0].Answer == nil || parsed.Result.Items[0].Answer.Code != "GROUNDED" {
		t.Fatal(before.Body.String())
	}
	answer := parsed.Result.Items[0].Answer
	for _, source := range answer.Sources {
		if _, e := store.Pool.Exec(ctx, `UPDATE documents SET roles=ARRAY['manager']::text[] WHERE id=$1`, source.ID); e != nil {
			t.Fatal(e)
		}
	}
	chat := request(h, "GET", "/api/chat", "", "", cookie)
	if !strings.Contains(chat.Body.String(), "Прежний ответ скрыт") {
		t.Fatal("history protection not reached")
	}
	after := request(h, "GET", "/api/jobs/"+job, "", "", cookie)
	t.Logf("chat hides answer; job status=%d unchanged=%v", after.Code, before.Body.String() == after.Body.String())
	if after.Code == 200 && strings.Contains(after.Body.String(), "GROUNDED") {
		t.Fatal("revoked document answer still exposed by job")
	}
}

func TestAstraOldRequestMustNotUndoNewGrant(t *testing.T) {
	store, b, _, _ := businessFixture(t)
	ctx := context.Background()
	now := b.Tools.Clock.Now()
	old := actions.Object{Service: "access", ID: "ACC-70001", UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для знакомства с командой", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: now, UpdatedAt: now}
	if e := store.ApplyObject(ctx, "alina", old, now); e != nil {
		t.Fatal(e)
	}
	next := old
	next.ID = "ACC-70002"
	next.Status = "approved"
	next.Version = 3
	next.CreatedAt = now.Add(time.Minute)
	next.UpdatedAt = now.Add(2 * time.Minute)
	if e := store.ApplyObject(ctx, "alina", next, now); e != nil {
		t.Fatal(e)
	}
	old.Status = "rejected"
	old.Version = 2
	old.UpdatedAt = now.Add(30 * time.Second)
	if e := store.ApplyObject(ctx, "alina", old, now); e != nil {
		t.Fatal(e)
	}
	if e := store.ApplyObject(ctx, "alina", next, now); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := store.Pool.QueryRow(ctx, `SELECT status FROM plan_steps WHERE id='alina-P6'`).Scan(&state); e != nil {
		t.Fatal(e)
	}
	t.Logf("approved current request plus late rejected old request: P6=%s", state)
	if state != "done" {
		t.Fatal("older request overwrote aggregate plan despite newer approved request")
	}
}

func TestAstraAttemptRechecksResourceVisibility(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в monitoring")
	cards, e := store.Cards(ctx, "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 1 {
		t.Fatal(cards, e)
	}
	w := request(h, "POST", "/api/cards/"+cards[0].ID+"/confirm", `{"expected_version":1}`, "http://localhost:5173", cookie)
	var confirmed struct {
		ID string `json:"attempt_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &confirmed) != nil {
		t.Fatal(w.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE resources SET kind='hidden' WHERE id='monitoring'`); e != nil {
		t.Fatal(e)
	}
	card := request(h, "GET", "/api/cards/"+cards[0].ID, "", "", cookie)
	attempt := request(h, "GET", "/api/attempts/"+confirmed.ID, "", "", cookie)
	t.Logf("hidden resource card=%d attempt=%d", card.Code, attempt.Code)
	if card.Code != 404 {
		t.Fatal("card protection not reached")
	}
	if attempt.Code == 200 && strings.Contains(attempt.Body.String(), "monitoring") {
		t.Fatal("hidden resource still exposed via attempt")
	}
}

func TestReviewConfirmationInput(t *testing.T) {
	for _, text := range []string{"нет", "не подтверждаю", "да, но пока не отправляй", "Подтверждаю?", "да\nнет"} {
		t.Run(text, func(t *testing.T) {
			store, b, _, h := businessFixture(t)
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"igor"}`, "http://localhost:5173", nil).Result().Cookies()[0]
			ctx := context.Background()
			p, e := store.Profile(ctx, "igor")
			if e != nil {
				t.Fatal(e)
			}
			confirmation, e := store.StepConfirmation(ctx, p, "alina-P1", 1, "done", b.Tools.Clock.Now(), b.StepTTL, b.Tools.Location)
			if e != nil {
				t.Fatal(e)
			}
			state, _ := json.Marshal(DialogueState{Stage: "confirm_step", StepID: "alina-P1", ConfirmationID: confirmation.ID})
			if _, e = store.Pool.Exec(ctx, `INSERT INTO dialogue_state(user_id,payload) VALUES('igor',$1)`, state); e != nil {
				t.Fatal(e)
			}
			b.Provider = astraConfirmProvider{}
			send := func(text string) {
				t.Helper()
				raw, _ := json.Marshal(map[string]string{"client_message_id": actions.ID(), "text": text})
				response := request(h, "POST", "/api/chat/messages", string(raw), "http://localhost:5173", cookie)
				if response.Code != 202 {
					t.Fatal(response.Code, response.Body.String())
				}
				if e := b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
					t.Fatal(e)
				}
			}
			send(text)
			var status string
			if e = store.Pool.QueryRow(ctx, `SELECT status FROM plan_steps WHERE id='alina-P1'`).Scan(&status); e != nil || status == "done" {
				t.Fatal(status, e)
			}
			send(" ДА ")
			if e = store.Pool.QueryRow(ctx, `SELECT status FROM plan_steps WHERE id='alina-P1'`).Scan(&status); e != nil || status != "done" {
				t.Fatal("explicit consent failed", status, e)
			}
		})
	}
}

func TestReviewCancellationVisibility(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			ctx := context.Background()
			now := b.Tools.Clock.Now()
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
			object := actions.Object{ID: "ACC-72001", Service: "access", UserID: "alina", ResourceID: "monitoring", Level: "read", Justification: "Для знакомства с командой", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: now, UpdatedAt: now}
			remote.objects[object.ID] = object
			if cached {
				if e := store.ApplyObject(ctx, "alina", object, now); e != nil {
					t.Fatal(e)
				}
			}
			created := request(h, "POST", "/api/cards", `{"type":"access_cancel","parameters":{"request_id":"ACC-72001","expected_version":1}}`, "http://localhost:5173", cookie)
			var result struct {
				Card actions.Card `json:"card"`
			}
			if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &result) != nil {
				t.Fatal(created.Code, created.Body.String())
			}
			confirmed := request(h, "POST", "/api/cards/"+result.Card.ID+"/confirm", `{"expected_version":1}`, "http://localhost:5173", cookie)
			var attempt struct {
				ID string `json:"attempt_id"`
			}
			if confirmed.Code != 202 || json.Unmarshal(confirmed.Body.Bytes(), &attempt) != nil {
				t.Fatal(confirmed.Code, confirmed.Body.String())
			}
			path := "/api/attempts/" + attempt.ID
			if before := request(h, "GET", path, "", "", cookie); before.Code != 200 {
				t.Fatal(before.Body.String())
			}
			if _, e := store.Pool.Exec(ctx, `UPDATE resources SET kind='hidden' WHERE id='monitoring'`); e != nil {
				t.Fatal(e)
			}
			for _, path := range []string{path, "/api/cards/" + result.Card.ID} {
				after := request(h, "GET", path, "", "", cookie)
				if after.Code != 404 || strings.Contains(after.Body.String(), object.ID) {
					t.Fatal(path, after.Code, after.Body.String())
				}
			}
		})
	}
}

func TestReviewPersistedPayloadCopies(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	p, e := store.Profile(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	payloads := []string{
		`{"items":[{"access_form":{"resource_id":"monitoring"}}]}`,
		`{"items":[{"card":{"parameters":{"resource_id":"monitoring"},"attempt":{"result":{"object":{"resource_id":"monitoring"}}}}}]}`,
		`{"kind":"success","object":{"resource_id":"monitoring"}}`,
		`{"items":[{"resources":[{"id":"monitoring","name":"Закрываемое имя"}]}]}`,
	}
	for _, raw := range payloads {
		if e := b.authorizePayload(ctx, p, json.RawMessage(raw)); e != nil {
			t.Fatal("visible payload rejected", e)
		}
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE resources SET kind='hidden' WHERE id='monitoring'`); e != nil {
		t.Fatal(e)
	}
	for _, raw := range payloads {
		id := actions.ID()
		if _, e := store.Pool.Exec(ctx, `INSERT INTO jobs(id,user_id,kind,state,result,available_at,created_at) VALUES($1,'alina','refresh','done',$2,now(),now())`, id, raw); e != nil {
			t.Fatal(e)
		}
		after := request(h, "GET", "/api/jobs/"+id, "", "", cookie)
		if after.Code != 404 || strings.Contains(after.Body.String(), "monitoring") {
			t.Fatal(after.Code, after.Body.String())
		}
	}
}

func TestReviewRequestProjectionOrder(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			store, b, _, _ := businessFixture(t)
			ctx := context.Background()
			now := b.Tools.Clock.Now()
			old := actions.Object{Service: "access", ID: "ACC-73001", UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для знакомства с командой", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: now, UpdatedAt: now}
			rejected := old
			rejected.Status = "rejected"
			rejected.Version = 2
			rejected.UpdatedAt = now.Add(time.Minute)
			next := old
			next.ID = "ACC-73002"
			next.Status = "approved"
			next.Version = 3
			next.CreatedAt = now.Add(2 * time.Minute)
			next.UpdatedAt = now.Add(3 * time.Minute)
			objects := []actions.Object{old, rejected, next}
			for _, i := range order {
				if e := store.ApplyObject(ctx, "alina", objects[i], now); e != nil {
					t.Fatal(e)
				}
			}
			var state string
			var version int
			if e := store.Pool.QueryRow(ctx, `SELECT status,version FROM plan_steps WHERE id='alina-P6'`).Scan(&state, &version); e != nil || state != "done" {
				t.Fatal(state, e)
			}
			if e := store.ApplyObject(ctx, "alina", next, now); e != nil {
				t.Fatal(e)
			}
			var after int
			if e := store.Pool.QueryRow(ctx, `SELECT version FROM plan_steps WHERE id='alina-P6'`).Scan(&after); e != nil || after != version {
				t.Fatal("repeat changed aggregate", version, after, e)
			}
		})
	}
}

func TestReviewOutsideAccessConsentPreparesOnly(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в launch-plans")
	postText(t, b, h, cookie, "да")
	raw, e := store.Dialogue(context.Background(), "alina")
	var state DialogueState
	if e != nil || json.Unmarshal(raw, &state) != nil || state.OutsideAccepted != "launch-plans" || state.Stage != "parameters" || remote.count != 0 {
		t.Fatal(string(raw), e, remote.count)
	}
}

func TestReviewHiddenDraftDiscard(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в monitoring")
	cards, e := store.Cards(ctx, "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 1 {
		t.Fatal(cards, e)
	}
	if _, e = store.Pool.Exec(ctx, `UPDATE resources SET kind='hidden' WHERE id='monitoring'`); e != nil {
		t.Fatal(e)
	}
	response := request(h, "POST", "/api/cards/"+cards[0].ID+"/discard", `{"expected_version":1}`, "http://localhost:5173", cookie)
	if response.Code != 404 || strings.Contains(response.Body.String(), "monitoring") {
		t.Fatal(response.Code, response.Body.String())
	}
}

type reviewCompoundProvider struct{ agent.Fake }

func (reviewCompoundProvider) Recognize(context.Context, agent.Context, string) ([]byte, error) {
	return []byte(`{"intents":[{"name":"find_doc","slots":{},"confidence":1},{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`), nil
}
func TestReviewCompoundModelAndExplicitControl(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в monitoring")
	b.Provider = reviewCompoundProvider{}
	postText(t, b, h, cookie, "Не отправляй, покажи инструкцию")
	var count int
	if e := store.Pool.QueryRow(ctx, `SELECT count(*) FROM attempts`).Scan(&count); e != nil || count != 0 {
		t.Fatal("model created consent", count, e)
	}
	postText(t, b, h, cookie, " ДА, ОТПРАВЛЯЙ ")
	worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second}}
	if e := worker.Once(ctx, store.OperationalClock.Now()); e != nil || remote.count != 1 {
		t.Fatal("explicit consent did not execute exactly once", remote.count, e)
	}
}
