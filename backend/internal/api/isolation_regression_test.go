package api

// Regression tests for the data isolation review (output/isolation-review):
// IR-01 service object owner, IR-02 OfferNext owner, IR-03 personal message
// ids, IR-04 tab profile header.

import (
	"ai-buddy/backend/internal/actions"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const isolationOrigin = "http://localhost:5173"

func isolationLogin(t *testing.T, h http.Handler, user string) *http.Cookie {
	t.Helper()
	w := request(h, "POST", "/api/demo/session", `{"user_id":"`+user+`"}`, isolationOrigin, nil)
	if w.Code != 200 {
		t.Fatalf("login %s: %d", user, w.Code)
	}
	return w.Result().Cookies()[0]
}

func isolationCard(t *testing.T, h http.Handler, ck *http.Cookie, body string) actions.Card {
	t.Helper()
	w := request(h, "POST", "/api/cards", body, isolationOrigin, ck)
	var out struct {
		Card actions.Card `json:"card"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("card: %d %s", w.Code, w.Body.String())
	}
	return out.Card
}

func TestIsolationServiceObjectKeepsOwner(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina := isolationLogin(t, h, "alina")
	maxim := isolationLogin(t, h, "maxim")
	created := now.Add(-time.Hour)
	owned := map[string]actions.Object{
		"time_off": {Service: "time_off", ID: "ABS-2001", UserID: "alina", StartDate: "2026-11-16", EndDate: "2026-11-16", Workdays: 1, ApproverID: "olga", Status: "submitted", Version: 1, CreatedAt: created, UpdatedAt: created},
		"access":   {Service: "access", ID: "ACC-5001", UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для задач стажировки в платежах", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: created, UpdatedAt: created},
	}
	for _, o := range owned {
		if e := store.ApplyObject(ctx, "alina", o, now); e != nil {
			t.Fatal(e)
		}
	}
	alinaMessages := func() int { items, _ := store.Messages(ctx, "alina", 0); return len(items) }
	alinaBefore := alinaMessages()
	planBefore := request(h, "GET", "/api/onboarding", "", "", alina).Body.String()
	reason := "Причина Максима"
	foreign := []actions.Object{
		{Service: "time_off", ID: "ABS-2001", UserID: "maxim", StartDate: "2026-11-23", EndDate: "2026-11-27", Workdays: 5, ApproverID: "anna", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now},
		{Service: "time_off", ID: "ABS-2001", UserID: "maxim", StartDate: "2026-11-23", EndDate: "2026-11-23", Workdays: 1, ApproverID: "anna", Status: "submitted", Version: 1, CreatedAt: now, UpdatedAt: now},
		{Service: "access", ID: "ACC-5001", UserID: "maxim", ResourceID: "space-search", Level: "edit", Justification: "Для задачи Максима в поиске", ApproverID: "anna", Status: "in_review", Version: 2, CreatedAt: now, UpdatedAt: now},
	}
	for _, o := range foreign {
		if e := store.ApplyObject(ctx, "maxim", o, now); e != actions.ErrContract {
			t.Errorf("%s v%d for maxim: %v", o.ID, o.Version, e)
		}
	}
	var leaked int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM service_objects WHERE user_id<>payload->>'user_id' OR user_id='maxim'`).Scan(&leaked)
	if leaked != 0 || alinaMessages() != alinaBefore || request(h, "GET", "/api/onboarding", "", "", alina).Body.String() != planBefore {
		t.Fatalf("foreign object changed alina's projection: leaked=%d", leaked)
	}
	// Refresh: the service lists a reused number for maxim; the job reports unavailability.
	remote.mu.Lock()
	remote.objects["ABS-2001"] = foreign[0]
	remote.mu.Unlock()
	if w := request(h, "POST", "/api/requests/refresh", `{"kind":"time_off"}`, isolationOrigin, maxim); w.Code != 202 {
		t.Fatal(w.Body.String())
	} else {
		var job struct {
			ID string `json:"job_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &job)
		if e := b.ProcessOnce(ctx, now); e != nil {
			t.Fatal(e)
		}
		j, _ := store.Job(ctx, "maxim", job.ID)
		if j.State != "failed" || j.Error != "SERVICE_UNAVAILABLE" {
			t.Errorf("refresh with reused number: %+v", j)
		}
	}
	if strings.Contains(request(h, "GET", "/api/onboarding", "", "", alina).Body.String(), "2026-11-23") {
		t.Error("alina plan shows maxim's dates after refresh")
	}
	// Confirmed action: the service answers maxim with alina's number.
	remote.mu.Lock()
	delete(remote.objects, "ABS-2001")
	remote.mu.Unlock()
	card := isolationCard(t, h, maxim, `{"type":"access_create","parameters":{"resource_id":"tracker-srch","level":"edit","justification":"Для задач стажировки в поиске"}}`)
	w := request(h, "POST", "/api/cards/"+card.ID+"/confirm", `{"expected_version":1}`, isolationOrigin, maxim)
	var conf struct {
		Attempt string `json:"attempt_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &conf) != nil {
		t.Fatal(w.Body.String())
	}
	worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Hour}}
	if e := worker.Once(ctx, now); e != nil {
		t.Fatal(e)
	}
	a, _ := store.Attempt(ctx, "maxim", conf.Attempt)
	if a.State != "unknown" {
		t.Errorf("attempt with reused number: state %s", a.State)
	}
	var payloadOwner string
	_ = store.Pool.QueryRow(ctx, `SELECT payload->>'user_id' FROM service_objects WHERE object_id='ACC-5001'`).Scan(&payloadOwner)
	if payloadOwner != "alina" {
		t.Errorf("ACC-5001 payload owner %s", payloadOwner)
	}
	// The owner's own newer version still updates monotonically.
	next := owned["time_off"]
	next.Version, next.Status, next.UpdatedAt = 2, "approved", now
	if e := store.ApplyObject(ctx, "alina", next, now); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(request(h, "GET", "/api/onboarding", "", "", alina).Body.String(), `"approved"`) {
		t.Error("own new version not applied")
	}
}

func TestIsolationOfferNextUsesOwnerRows(t *testing.T) {
	store, _, _, h := businessFixture(t)
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina := isolationLogin(t, h, "alina")
	maxim := isolationLogin(t, h, "maxim")
	request(h, "GET", "/api/chat", "", "", maxim)
	request(h, "GET", "/api/chat", "", "", alina)
	foreign := isolationCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`)
	if w := request(h, "POST", "/api/cards/"+foreign.ID+"/discard", `{"expected_version":1}`, isolationOrigin, alina); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	setState := func(user string, payload map[string]any) {
		raw, _ := json.Marshal(payload)
		if _, e := store.Pool.Exec(ctx, `INSERT INTO dialogue_state(user_id,payload) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET payload=EXCLUDED.payload`, user, raw); e != nil {
			t.Fatal(e)
		}
	}
	pending := []map[string]string{{"name": "time_off_request"}}
	count := func(user string) int { items, _ := store.Messages(ctx, user, 0); return len(items) }
	// A foreign card id in maxim's state is ignored.
	setState("maxim", map[string]any{"card_id": foreign.ID, "pending": pending, "stage": "waiting_result"})
	before := count("maxim")
	if offered, e := store.OfferNext(ctx, now); e != nil || offered || count("maxim") != before {
		t.Fatalf("foreign card produced an offer: offered=%v err=%v", offered, e)
	}
	// A foreign first transfer card never counts as a succeeded first action.
	own := isolationCard(t, h, maxim, `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`)
	if w := request(h, "POST", "/api/cards/"+own.ID+"/discard", `{"expected_version":1}`, isolationOrigin, maxim); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	succeeded := isolationCard(t, h, alina, `{"type":"time_off_create","parameters":{"start_date":"2026-11-16","end_date":"2026-11-16"}}`)
	if w := request(h, "POST", "/api/cards/"+succeeded.ID+"/confirm", `{"expected_version":1}`, isolationOrigin, alina); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE attempts SET state='succeeded' WHERE card_id=$1`, succeeded.ID); e != nil {
		t.Fatal(e)
	}
	setState("maxim", map[string]any{"card_id": own.ID, "pending": pending, "stage": "waiting_result", "transfer": map[string]string{"first_card_id": succeeded.ID}})
	if offered, e := store.OfferNext(ctx, now); e != nil || !offered {
		t.Fatalf("own state not processed: %v %v", offered, e)
	}
	items, _ := store.Messages(ctx, "maxim", 0)
	last := items[len(items)-1].Text
	if !strings.Contains(last, "Первое действие переноса не завершилось успехом") {
		t.Errorf("foreign transfer card treated as succeeded: %q", last)
	}
	// The owner's own next action is still offered.
	alinaBefore := count("alina")
	setState("alina", map[string]any{"card_id": foreign.ID, "pending": pending, "stage": "waiting_result"})
	if offered, e := store.OfferNext(ctx, now); e != nil || !offered || count("alina") != alinaBefore+1 {
		t.Fatalf("own offer missing: %v %v", offered, e)
	}
}

func TestIsolationMessageIDsArePersonal(t *testing.T) {
	_, b, _, h := businessFixture(t)
	alina := isolationLogin(t, h, "alina")
	maxim := isolationLogin(t, h, "maxim")
	post := func(ck *http.Cookie, client, text string) int64 {
		raw, _ := json.Marshal(map[string]string{"client_message_id": client, "text": text})
		w := request(h, "POST", "/api/chat/messages", string(raw), isolationOrigin, ck)
		var out struct {
			ID int64 `json:"message_id"`
		}
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Body.String())
		}
		return out.ID
	}
	firstClient := actions.ID()
	first := post(maxim, firstClient, "Привет")
	for i := 0; i < 3; i++ {
		post(alina, actions.ID(), "Привет")
	}
	second := post(maxim, actions.ID(), "Привет снова")
	if second != first+1 {
		t.Errorf("maxim ids %d -> %d reveal foreign messages", first, second)
	}
	if replay := post(maxim, firstClient, "Привет"); replay != first {
		t.Errorf("replay id %d, want %d", replay, first)
	}
	raw, _ := json.Marshal(map[string]any{"client_message_id": actions.ID(), "intent": "next_step", "slots": map[string]string{}})
	var button struct {
		IDs []int64 `json:"message_ids"`
	}
	_ = json.Unmarshal(request(h, "POST", "/api/dialogue/actions", string(raw), isolationOrigin, maxim).Body.Bytes(), &button)
	if len(button.IDs) != 1 || button.IDs[0] != second+1 {
		t.Errorf("button message ids %v", button.IDs)
	}
	for range 10 {
		_ = b.ProcessOnce(context.Background(), b.Store.OperationalClock.Now())
	}
	var chat struct {
		Items []struct {
			ID       int64 `json:"id"`
			Sequence int64 `json:"sequence"`
		} `json:"items"`
	}
	_ = json.Unmarshal(request(h, "GET", "/api/chat", "", "", maxim).Body.Bytes(), &chat)
	if len(chat.Items) < 5 {
		t.Fatalf("maxim feed too short: %d", len(chat.Items))
	}
	for i, item := range chat.Items {
		if item.ID != int64(i+1) || item.ID != item.Sequence {
			t.Errorf("feed position %d has id %d sequence %d", i+1, item.ID, item.Sequence)
		}
	}
}

func TestIsolationTabProfileHeader(t *testing.T) {
	store, _, _, h := businessFixture(t)
	ctx := context.Background()
	alina := isolationLogin(t, h, "alina")
	card := isolationCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки в платежах"}}`)
	id := actions.ID()
	routes := []struct{ method, path, body string }{
		{"GET", "/api/me", ""}, {"GET", "/api/chat", ""}, {"GET", "/api/onboarding", ""}, {"GET", "/api/executions", ""},
		{"GET", "/api/executions/" + id, ""}, {"GET", "/api/cards/" + card.ID, ""}, {"GET", "/api/attempts/" + id, ""}, {"GET", "/api/jobs/" + id, ""},
		{"GET", "/demo/docs/doc-pay-principles", ""},
		{"POST", "/api/chat/messages", `{"client_message_id":"` + actions.ID() + `","text":"Привет"}`},
		{"POST", "/api/dialogue/actions", `{"client_message_id":"` + actions.ID() + `","intent":"next_step","slots":{}}`},
		{"POST", "/api/cards", `{"type":"time_off_create","parameters":{"start_date":"2026-11-16","end_date":"2026-11-16"}}`},
		{"PATCH", "/api/cards/" + card.ID, `{"expected_version":1,"changes":{"level":"edit"}}`},
		{"POST", "/api/cards/" + card.ID + "/discard", `{"expected_version":1}`},
		{"POST", "/api/cards/" + card.ID + "/confirm", `{"expected_version":1}`},
		{"POST", "/api/attempts/" + id + "/reconcile", `{}`}, {"POST", "/api/attempts/" + id + "/retry", `{}`},
		{"POST", "/api/requests/refresh", `{"kind":"access"}`},
		{"POST", "/api/steps/alina-P5/confirmation", `{"expected_version":1,"target_state":"done"}`},
		{"POST", "/api/steps/alina-P5/confirm", `{"confirmation_id":"` + id + `"}`},
	}
	counts := func() string {
		var m, c, j, s int
		_ = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM messages),(SELECT count(*) FROM cards),(SELECT count(*) FROM jobs),(SELECT count(*) FROM step_confirmations)`).Scan(&m, &c, &j, &s)
		return strconv.Itoa(m) + "/" + strconv.Itoa(c) + "/" + strconv.Itoa(j) + "/" + strconv.Itoa(s)
	}
	send := func(method, path, body, profile string, ck *http.Cookie) (int, string) {
		origin := ""
		if method != "GET" {
			origin = isolationOrigin
		}
		r := newRequest(method, path, body, origin, ck)
		if profile != "" {
			r.Header.Set("X-Buddy-Profile", profile)
		}
		w := serve(h, r)
		return w.Code, w.Body.String()
	}
	before := counts()
	for _, rt := range routes {
		code, body := send(rt.method, rt.path, rt.body, "maxim", alina)
		if code != 409 || !strings.Contains(body, "PROFILE_CHANGED") || strings.Contains(body, "alina") || strings.Contains(body, "Алина") {
			t.Errorf("%s %s with foreign tab profile: %d %s", rt.method, rt.path, code, body)
		}
	}
	if after := counts(); after != before {
		t.Errorf("refused requests changed state: %s -> %s", before, after)
	}
	if c, _ := send("GET", "/api/chat", "", "alina", alina); c != 200 {
		t.Errorf("matching profile: %d", c)
	}
	if c, _ := send("GET", "/api/chat", "", "", alina); c != 200 {
		t.Errorf("no profile header: %d", c)
	}
	if c, _ := send("GET", "/api/me", "", "alina", nil); c != 401 {
		t.Errorf("no session: %d", c)
	}
	pre := newRequest("OPTIONS", "/api/chat/messages", "", isolationOrigin, nil)
	w := serve(h, pre)
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "X-Buddy-Profile") {
		t.Errorf("CORS does not allow profile header: %q", w.Header().Get("Access-Control-Allow-Headers"))
	}
}

func newRequest(method, path, body, origin string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
