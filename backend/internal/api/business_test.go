package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/internal/catalog"
	"ai-buddy/backend/internal/integrations"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/internal/tools"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRemote struct {
	mu         sync.Mutex
	objects    map[string]actions.Object
	operations map[string]actions.Object
	fail       bool
	unknown    bool
	count      int
	granted    bool
}

func (f *fakeRemote) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user := r.Header.Get("X-Actor-ID")
	if user == "" || r.Header.Get("Authorization") != "Bearer synthetic-service-secret-32-bytes" {
		return reply(401, map[string]any{"error": map[string]string{"code": "UNAUTHENTICATED"}}), nil
	}
	if f.fail {
		return reply(503, map[string]any{"error": map[string]string{"code": "SERVICE_UNAVAILABLE"}}), nil
	}
	if r.URL.Path == "/v1/resources" {
		items := []integrations.Resource{}
		for _, res := range demo.Resources() {
			if res.Kind == "hidden" {
				continue
			}
			owner, level := res.ApproverID, res.DefaultLevel
			items = append(items, integrations.Resource{ID: res.ID, Name: res.Name, Aliases: res.Synonyms, CanRequest: res.Kind != "closed", Levels: res.Levels, DefaultLevel: &level, Teams: res.RecommendedTeams, RequiresTask: res.Kind == "task", ApproverID: &owner})
		}
		return reply(200, map[string]any{"items": items}), nil
	}
	if r.URL.Path == "/v1/grants" {
		if f.granted {
			return reply(200, map[string]any{"items": []integrations.Grant{{ResourceID: "monitoring", Level: "read", GrantedAt: time.Now(), SourceRequestID: "ACC-111"}}}), nil
		}
		return reply(200, map[string]any{"items": []any{}}), nil
	}
	service := "access"
	if strings.Contains(r.URL.Path, "time-off") || r.Header.Get("X-Service") == "time_off" {
		service = "time_off"
	}
	if strings.Contains(r.URL.Path, "/operations/") {
		key := strings.Split(r.URL.Path, "/")[3]
		object, ok := f.operations[key]
		if ok {
			return reply(200, map[string]any{"state": "completed", "operation_key": key, "result": map[string]any{"kind": "success", "http_status": 201, "body": object}}), nil
		}
		return reply(200, map[string]any{"state": "not_created", "operation_key": key}), nil
	}
	if r.Method == "GET" {
		if strings.HasPrefix(r.URL.Path, "/v1/access-requests/") || strings.HasPrefix(r.URL.Path, "/v1/time-off-requests/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			object, ok := f.objects[id]
			if !ok || object.UserID != user {
				return reply(404, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}}), nil
			}
			return reply(200, object), nil
		}
		items := []actions.Object{}
		for _, object := range f.objects {
			if object.Service == service && object.UserID == user {
				items = append(items, object)
			}
		}
		return reply(200, map[string]any{"items": items}), nil
	}
	var input map[string]json.RawMessage
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		return reply(400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR"}}), nil
	}
	var key string
	_ = json.Unmarshal(input["operation_key"], &key)
	if old, ok := f.operations[key]; ok {
		return reply(200, old), nil
	}
	f.count++
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	object := actions.Object{Service: service, UserID: user, ApproverID: "olga", Version: 1, Status: "created", CreatedAt: now, UpdatedAt: now}
	if service == "access" {
		object.ID = fmt.Sprintf("ACC-%d", 5000+f.count)
		_ = json.Unmarshal(input["resource_id"], &object.ResourceID)
		_ = json.Unmarshal(input["level"], &object.Level)
		_ = json.Unmarshal(input["justification"], &object.Justification)
	} else {
		object.ID = fmt.Sprintf("ABS-%d", 5000+f.count)
		object.Status = "submitted"
		object.Workdays = 1
		_ = json.Unmarshal(input["start_date"], &object.StartDate)
		_ = json.Unmarshal(input["end_date"], &object.EndDate)
	}
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		parts := strings.Split(r.URL.Path, "/")
		object = f.objects[parts[len(parts)-2]]
		object.Status = "cancelled"
		object.Version++
	}
	f.objects[object.ID] = object
	f.operations[key] = object
	if f.unknown {
		return nil, fmt.Errorf("synthetic lost response with private body")
	}
	return reply(201, object), nil
}
func reply(status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}
}

type testDirectory struct {
	store  *storage.Store
	client integrations.Client
}

func (d testDirectory) Profile(ctx context.Context, user string) (demo.Profile, error) {
	return d.store.Profile(ctx, user)
}
func (d testDirectory) Resources(ctx context.Context, p demo.Profile) ([]demo.Resource, error) {
	return d.client.Directory(ctx, p)
}
func businessFixture(t *testing.T) (*storage.Store, *Business, *fakeRemote, http.Handler) {
	t.Helper()
	databaseURL := os.Getenv("TEST_API_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("make coverage starts PostgreSQL")
	}
	ctx := context.Background()
	admin, e := storage.Open(ctx, databaseURL)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"api_" + strings.ReplaceAll(actions.ID(), "-", "")}.Sanitize()
	if _, e = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	parsed, e := url.Parse(databaseURL)
	if e != nil {
		t.Fatal(e)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	store, e := storage.Open(ctx, parsed.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(store.Close)
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = store.Seed(ctx); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	store.OperationalClock = clock.Fixed{Instant: now}
	remote := &fakeRemote{objects: map[string]actions.Object{}, operations: map[string]actions.Object{}}
	client := integrations.Client{BaseURL: "http://synthetic.test", Secret: "synthetic-service-secret-32-bytes", HTTP: &http.Client{Transport: remote}, Timeout: time.Second}
	b := &Business{Store: store, Tools: tools.Client{Authority: store, Remote: client, Clock: clock.Fixed{Instant: now}, Location: time.FixedZone("demo", 5*3600)}, Provider: agent.Fake{}, LLMTimeout: time.Second, CardTTL: 24 * time.Hour, StepTTL: 5 * time.Minute}
	b.Builder = agent.Builder{Source: store, Directory: catalog.Directory{Source: testDirectory{store, client}}}
	b.Tools.Source = store
	b.Tools.Directory = b.Builder.Directory
	return store, b, remote, New(store, Config{Demo: true, Origin: "http://localhost:5173", Clock: b.Tools.Clock, Location: b.Tools.Location, SessionTTL: time.Hour, Business: b})
}
func TestBusinessAccessLifecycleAndReload(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil)
	cookie := login.Result().Cookies()[0]
	body := `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки"}}`
	w := request(h, "POST", "/api/cards", body, origin, cookie)
	var card struct {
		Card actions.Card `json:"card"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &card) != nil {
		t.Fatal(w)
	}
	// Reads and mutations cannot switch identity through parameters.
	other := request(h, "POST", "/api/demo/session", `{"user_id":"maxim"}`, origin, nil).Result().Cookies()[0]
	if w := request(h, "GET", "/api/cards/"+card.Card.ID, "", "", other); w.Code != 404 {
		t.Fatal(w)
	}
	if w := request(h, "PATCH", "/api/cards/"+card.Card.ID, `{"expected_version":99,"changes":{"level":"read"}}`, origin, cookie); w.Code != 409 {
		t.Fatal(w)
	}
	if w := request(h, "PATCH", "/api/cards/"+card.Card.ID, `{"expected_version":1,"changes":{"justification":"Для изучения команды и задач"}}`, origin, cookie); w.Code != 200 {
		t.Fatal(w)
	}
	confirmed := request(h, "POST", "/api/cards/"+card.Card.ID+"/confirm", `{"expected_version":2}`, origin, cookie)
	var a struct {
		Attempt string `json:"attempt_id"`
		Job     string `json:"job_id"`
	}
	if confirmed.Code != 202 || json.Unmarshal(confirmed.Body.Bytes(), &a) != nil {
		t.Fatal(confirmed)
	}
	same := request(h, "POST", "/api/cards/"+card.Card.ID+"/confirm", `{"expected_version":2}`, origin, cookie)
	if same.Code != 202 || same.Body.String() != confirmed.Body.String() {
		t.Fatal(same, confirmed)
	}
	worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}}
	if e := worker.Once(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
	if w := request(h, "GET", "/api/attempts/"+a.Attempt, "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "succeeded") {
		t.Fatal(w)
	}
	if w := request(h, "GET", "/api/jobs/"+a.Job, "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "completed") {
		t.Fatal(w)
	}
	remote.mu.Lock()
	for id, obj := range remote.objects {
		obj.Status = "approved"
		obj.Version++
		remote.objects[id] = obj
	}
	remote.mu.Unlock()
	if e := b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
	plan := request(h, "GET", "/api/onboarding", "", "", cookie)
	if plan.Code != 200 || !strings.Contains(plan.Body.String(), "approved") {
		t.Fatal(plan)
	}
	steps, e := store.Steps(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	for _, step := range steps {
		if step.Code == "P6" && step.Status != "done" {
			t.Fatal("grant did not project")
		}
	}
	history := request(h, "GET", "/api/chat", "", "", cookie)
	if history.Code != 200 || !strings.Contains(history.Body.String(), "ACC-") {
		t.Fatal(history)
	}
	if w := request(h, "POST", "/api/cards", body, origin, cookie); w.Code != 200 { // Remote fake does not synthesize grants; existing approved requests are terminal.
		t.Fatal(w)
	}
	if w := request(h, "GET", "/api/jobs/"+a.Job, "", "", other); w.Code != 404 {
		t.Fatal(w)
	}
	if w := request(h, "POST", "/api/attempts/"+a.Attempt+"/reconcile", `{}`, origin, cookie); w.Code != 409 {
		t.Fatal(w)
	}
	if w := request(h, "POST", "/api/attempts/"+a.Attempt+"/retry", `{}`, origin, cookie); w.Code != 409 {
		t.Fatal(w)
	}
}

func finishMessage(t *testing.T, b *Business, id string) {
	t.Helper()
	for range 15 {
		j, e := b.Store.Job(context.Background(), "alina", id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == "completed" || j.State == "failed" {
			return
		}
		if e = b.ProcessOnce(context.Background(), b.Store.OperationalClock.Now()); e != nil {
			t.Fatal(e)
		}
	}
	t.Fatal("message did not finish")
}
func postText(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie, text string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"client_message_id": actions.ID(), "text": text})
	w := request(h, "POST", "/api/chat/messages", string(raw), "http://localhost:5173", cookie)
	var body struct {
		Job string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w)
	}
	finishMessage(t, b, body.Job)
	return body.Job
}
func TestDialogueQueueTopicChangeAndExplicitConfirm(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	postText(t, b, h, cookie, "Нужен доступ в monitoring и отгул в пятницу")
	cards, e := store.Cards(ctx, "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 1 || cards[0].Type != "access_create" {
		t.Fatal(cards, e)
	}
	card := cards[0]
	raw, e := store.Dialogue(ctx, "alina")
	var state DialogueState
	if e != nil || json.Unmarshal(raw, &state) != nil || len(state.Pending) != 1 {
		t.Fatal(string(raw), e)
	}
	postText(t, b, h, cookie, "С чего начать?")
	postText(t, b, h, cookie, "да")
	if remote.count != 0 {
		t.Fatal("ambiguous yes sent request")
	}
	w := request(h, "POST", "/api/cards/"+card.ID+"/confirm", `{"expected_version":1}`, origin, cookie)
	if w.Code != 202 {
		t.Fatal(w)
	}
	worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}}
	if e = worker.Once(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
	if e = b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
	history := request(h, "GET", "/api/chat", "", "", cookie)
	if history.Code != 200 || !strings.Contains(history.Body.String(), "Перейдём к отгулу") {
		t.Fatal(history)
	}
	postText(t, b, h, cookie, "да")
	cards, e = store.Cards(ctx, "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 1 || cards[0].Type != "time_off_create" {
		t.Fatal(cards, e)
	}
	postText(t, b, h, cookie, "не надо")
	cards, e = store.Cards(ctx, "alina", b.Tools.Clock.Now())
	if e != nil || len(cards) != 0 || remote.count != 1 {
		t.Fatal("cancelled draft caused effect", cards, e)
	}
}
func TestAPIAbsenceStepsDocumentsAndSensitiveCorpus(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	for _, path := range []string{"/api/cards/bad", "/api/attempts/bad", "/api/jobs/bad", "/demo/docs/doc-srch-components"} {
		if w := request(h, "GET", path, "", "", cookie); w.Code != 404 {
			t.Fatal(path, w)
		}
	}
	if w := request(h, "GET", "/demo/docs/doc-tracker", "", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "Демонстрационная инструкция") {
		t.Fatal(w)
	}
	if w := request(h, "GET", "/api/chat?after=bad", "", "", cookie); w.Code != 400 {
		t.Fatal(w)
	}
	for _, body := range []string{`{"type":"time_off_create","parameters":{"start_date":"2026-11-13","end_date":"2026-11-13","reason":"private"}}`, `{"type":"access_create","parameters":{"resource_id":"salary-reports","level":"read","justification":"Для задач стажировки"}}`, `{"type":"access_create","parameters":{"resource_id":"security-incidents","level":"read","justification":"Для задач стажировки"}}`} {
		if w := request(h, "POST", "/api/cards", body, origin, cookie); w.Code != 400 && w.Code != 403 && w.Code != 404 {
			t.Fatal(w)
		}
	}
	w := request(h, "POST", "/api/cards", `{"type":"time_off_create","parameters":{"start_date":"2026-11-13","end_date":"2026-11-13"}}`, origin, cookie)
	var c struct {
		Card actions.Card `json:"card"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatal(w)
	}
	w = request(h, "POST", "/api/cards/"+c.Card.ID+"/discard", `{"expected_version":1}`, origin, cookie)
	if w.Code != 200 {
		t.Fatal(w)
	}
	w = request(h, "POST", "/api/steps/alina-P5/confirmation", `{"expected_version":1,"target_state":"done"}`, origin, cookie)
	var confirmation storage.StepConfirmation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &confirmation) != nil {
		t.Fatal(w)
	}
	body := fmt.Sprintf(`{"confirmation_id":%q}`, confirmation.ID)
	first := request(h, "POST", "/api/steps/alina-P5/confirm", body, origin, cookie)
	repeat := request(h, "POST", "/api/steps/alina-P5/confirm", body, origin, cookie)
	if first.Code != 200 || first.Body.String() != repeat.Body.String() {
		t.Fatal(first, repeat)
	}
	for _, id := range []string{"maxim-P5", "alina-P6", "alina-P7"} {
		w := request(h, "POST", "/api/steps/"+id+"/confirmation", `{"expected_version":1,"target_state":"done"}`, origin, cookie)
		if w.Code != 403 && w.Code != 404 {
			t.Fatal(id, w)
		}
	}
	for _, text := range []string{"Отгул: диагноз диабет", "У мамы грипп", "Паспорт 1234 567890", "Нужен доступ token=synthetic-private-value"} {
		postText(t, b, h, cookie, text)
	}
	history := request(h, "GET", "/api/chat", "", "", cookie)
	for _, private := range []string{"диабет", "грипп", "1234 567890", "synthetic-private-value"} {
		if strings.Contains(history.Body.String(), private) {
			t.Fatal("sensitive corpus leaked")
		}
	}
	var raw string
	if e := store.Pool.QueryRow(ctx, `SELECT COALESCE(string_agg(row_to_json(m)::text,' '),'') FROM messages m WHERE user_id='alina'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "synthetic-private-value") {
		t.Fatal("raw text persisted")
	}
	if w := request(h, "POST", "/api/requests/refresh", `{"kind":"unknown"}`, origin, cookie); w.Code != 400 {
		t.Fatal(w)
	}
	if w := request(h, "POST", "/api/requests/refresh", `{"kind":"access","id":"ACC-missing"}`, origin, cookie); w.Code != 404 {
		t.Fatal(w)
	}
	w = request(h, "POST", "/api/requests/refresh", `{"kind":"access"}`, origin, cookie)
	if w.Code != 202 {
		t.Fatal(w)
	}
	if e := b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
}
func TestDialogueClarificationsAndButtons(t *testing.T) {
	_, b, _, h := businessFixture(t)
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	for _, text := range []string{"привет", "Где инструкция по отгулу?", "Кто наставник?", "Нужен доступ", "Нужен отгул", "на следующей неделе", "завтра", "не надо", "непонятно", "Оформи командировку", "что с моей заявкой", "поменяй", "я познакомилась с командой"} {
		postText(t, b, h, cookie, text)
	}
	for _, intent := range []string{"next_step", "find_doc", "who_to_ask", "time_off_info", "access_request", "time_off_request", "status", "mark_done", "card_confirm", "card_cancel", "card_edit", "out_of_scope", "smalltalk", "unclear"} {
		raw, _ := json.Marshal(map[string]any{"client_message_id": actions.ID(), "intent": intent, "slots": map[string]string{}})
		w := request(h, "POST", "/api/dialogue/actions", string(raw), origin, cookie)
		var body struct {
			Job string `json:"job_id"`
		}
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal(w)
		}
		finishMessage(t, b, body.Job)
	}
	history := request(h, "GET", "/api/chat", "", "", cookie)
	if history.Code != 200 || strings.Contains(history.Body.String(), "salary-reports") {
		t.Fatal(history)
	}
}

func TestTransferOrdersUnknownOutcomeAndDecline(t *testing.T) {
	for _, scenario := range []string{"nonoverlap", "overlap", "unknown_then_decline"} {
		t.Run(scenario, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			ctx := context.Background()
			origin := "http://localhost:5173"
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
			now := b.Tools.Clock.Now()
			remote.objects["ABS-9000"] = actions.Object{ID: "ABS-9000", Service: "time_off", UserID: "alina", StartDate: "2026-11-20", EndDate: "2026-11-20", Workdays: 1, ApproverID: "olga", Status: "submitted", Version: 1, CreatedAt: now, UpdatedAt: now}
			date := "2026-11-23"
			expected := "time_off_create"
			if scenario == "overlap" {
				date = "2026-11-20"
				expected = "time_off_cancel"
			}
			raw, _ := json.Marshal(map[string]any{"client_message_id": actions.ID(), "intent": "time_off_request", "slots": map[string]string{"request_id": "ABS-9000", "start_date": date, "end_date": date}})
			w := request(h, "POST", "/api/dialogue/actions", string(raw), origin, cookie)
			var queued struct {
				Job string `json:"job_id"`
			}
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &queued) != nil {
				t.Fatal(w)
			}
			finishMessage(t, b, queued.Job)
			routeIDs, e := store.ExecutionIDs(ctx, "alina", "job", queued.Job)
			if e != nil || len(routeIDs) == 0 {
				t.Fatal(routeIDs, e)
			}
			parentID := routeIDs[0]
			parent, e := (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", parentID, 0, 100)
			if e != nil || parent.Snapshot.Scenario != "transfer" || len(parent.Snapshot.Children) != 2 {
				t.Fatal("missing transfer branches", parent, e)
			}

			cards, e := store.Cards(ctx, "alina", now)
			if e != nil || len(cards) != 1 || cards[0].Type != expected {
				t.Fatal(cards, e)
			}
			worker := actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}}
			confirm := func(card actions.Card) {
				t.Helper()
				w := request(h, "POST", "/api/cards/"+card.ID+"/confirm", fmt.Sprintf(`{"expected_version":%d}`, card.Version), origin, cookie)
				if w.Code != 202 {
					t.Fatal(w)
				}
			}
			confirm(cards[0])
			remote.unknown = scenario == "unknown_then_decline"
			if e = worker.Once(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			if scenario == "unknown_then_decline" {
				parent, e = (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", parentID, 0, 100)
				if e != nil || parent.Snapshot.State != "reconciling" {
					t.Fatal("unknown transfer shown as final", parent, e)
				}
				if e = b.ProcessOnce(ctx, store.OperationalClock.Now()); e != actions.ErrNoWork {
					t.Fatal("unknown outcome advanced transfer", e)
				}
				postText(t, b, h, cookie, "да")
				if remote.count != 1 {
					t.Fatal("unknown transfer resent")
				}
				remote.unknown = false
				store.OperationalClock = clock.Fixed{Instant: now.Add(2 * time.Second)}
				if e = worker.Once(ctx, store.OperationalClock.Now()); e != nil {
					t.Fatal(e)
				}
			}
			if e = b.ProcessOnce(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			if scenario == "unknown_then_decline" {
				postText(t, b, h, cookie, "не надо")
				parent, e = (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", parentID, 0, 100)
				if e != nil || parent.Snapshot.State == "completed" || parent.Snapshot.ResultCode != "partial" {
					t.Fatal("declined second operation lost partial result", parent, e)
				}

				if remote.objects["ABS-9000"].Status != "submitted" || remote.count != 1 {
					t.Fatal("declined second action changed old request")
				}
				return
			}
			postText(t, b, h, cookie, "да")
			cards, e = store.Cards(ctx, "alina", now)
			if e != nil || len(cards) != 1 || cards[0].Type == expected {
				t.Fatal("second operation not separately prepared", cards, e)
			}
			if remote.count != 1 {
				t.Fatal("second operation executed before confirmation")
			}
			confirm(cards[0])
			if e = worker.Once(ctx, store.OperationalClock.Now()); e != nil {
				t.Fatal(e)
			}
			parent, e = (storage.ExecutionRepository{Store: store}).Read(ctx, "alina", parentID, 0, 100)
			if e != nil || parent.Snapshot.State != "completed" || len(parent.Snapshot.Children) != 2 {
				t.Fatal("transfer did not preserve two results", parent, e)
			}
			for _, node := range parent.Snapshot.Nodes {
				if node.State == "waiting_user" {
					t.Fatal("completed transfer still asks for confirmation", node)
				}
			}
			if remote.objects["ABS-9000"].Status != "cancelled" || remote.count != 2 {
				t.Fatal("transfer not completed", remote.objects)
			}
		})
	}
}

func TestReadToolsUseAuthoritativeIdentityAndClosedSchemas(t *testing.T) {
	_, b, _, _ := businessFixture(t)
	ctx := context.Background()
	p, e := b.Store.Profile(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ name, body string }{{"onboarding.get_plan", `{}`}, {"access.list_resources", `{"query":"monitoring"}`}, {"documents.search", `{"query":"отгул"}`}} {
		if _, e := b.Tools.Read(ctx, p, tc.name, json.RawMessage(tc.body)); e != nil {
			t.Fatal(tc, e)
		}
	}
	for _, tc := range []struct{ name, body string }{{"arbitrary.tool", `{}`}, {"onboarding.get_plan", `{"user_id":"maxim"}`}, {"documents.search", `{"query":"диагноз диабет"}`}, {"access.get_status", `{}`}, {"time_off.get_status", `{"request_id":"ABS-missing"}`}} {
		if _, e := b.Tools.Read(ctx, p, tc.name, json.RawMessage(tc.body)); e == nil {
			t.Fatal("tool accepted denied context", tc)
		}
	}
	p.Role = "manager"
	if _, e := b.Tools.Read(ctx, p, "onboarding.get_plan", json.RawMessage(`{}`)); e == nil {
		t.Fatal("forged role accepted")
	}
}

func TestRejectionNeedsExplicitContinuationAndNewJustification(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	origin := "http://localhost:5173"
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil).Result().Cookies()[0]
	now := b.Tools.Clock.Now()
	reason := "Уточни рабочую задачу"
	remote.objects["ACC-9000"] = actions.Object{ID: "ACC-9000", Service: "access", UserID: "alina", ResourceID: "monitoring", Level: "read", Justification: "Для задач стажировки", ApproverID: "olga", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
	w := request(h, "POST", "/api/cards", `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для новой рабочей задачи"}}`, origin, cookie)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "REAPPLY_CONFIRMATION_REQUIRED") {
		t.Fatal(w)
	}
	postText(t, b, h, cookie, "Нужен доступ в monitoring")
	cards, e := store.Cards(ctx, "alina", now)
	if e != nil || len(cards) != 0 {
		t.Fatal("rejected resource silently resubmitted", cards, e)
	}
	postText(t, b, h, cookie, "Для исследования метрик тестового сервиса")
	cards, e = store.Cards(ctx, "alina", now)
	if e != nil || len(cards) != 1 {
		t.Fatal(cards, e)
	}
	if _, e = store.CancelCard(ctx, "alina", cards[0].ID, cards[0].Version, now); e != nil {
		t.Fatal(e)
	}
	remote.objects["ABS-9000"] = actions.Object{ID: "ABS-9000", Service: "time_off", UserID: "alina", StartDate: "2026-11-20", EndDate: "2026-11-20", Workdays: 1, ApproverID: "olga", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
	postText(t, b, h, cookie, "Нужен отгул в пятницу 20-го")
	cards, e = store.Cards(ctx, "alina", now)
	if e != nil || len(cards) != 0 {
		t.Fatal("rejected dates silently resubmitted", cards, e)
	}
	postText(t, b, h, cookie, "да")
	cards, e = store.Cards(ctx, "alina", now)
	if e != nil || len(cards) != 1 || cards[0].Type != "time_off_create" {
		t.Fatal(cards, e)
	}
}
func TestStepOwnerProjectionTTLAndDuplicateConfirmation(t *testing.T) {
	store, b, _, _ := businessFixture(t)
	ctx := context.Background()
	p, e := store.Profile(ctx, "igor")
	if e != nil {
		t.Fatal(e)
	}
	now := b.Tools.Clock.Now()
	c, e := store.StepConfirmation(ctx, p, "alina-P1", 1, "done", now, b.StepTTL, b.Tools.Location)
	if e != nil {
		t.Fatal(e)
	}
	step, e := store.ConfirmStep(ctx, "igor", "alina-P1", c.ID, now, b.Tools.Location)
	if e != nil || step.Status != "done" {
		t.Fatal(step, e)
	}
	repeat, e := store.ConfirmStep(ctx, "igor", "alina-P1", c.ID, now.Add(10*time.Minute), b.Tools.Location)
	if e != nil || repeat.Version != step.Version {
		t.Fatal(repeat, e)
	}
	alinaSteps, e := store.Steps(ctx, "alina")
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range alinaSteps {
		if s.ID == "alina-P1" && s.Status != "done" {
			t.Fatal("buddy assignment not projected")
		}
	}
	c, e = store.StepConfirmation(ctx, p, "alina-P2", 1, "done", now, b.StepTTL, b.Tools.Location)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.ConfirmStep(ctx, "igor", "alina-P2", c.ID, now.Add(b.StepTTL), b.Tools.Location); e != actions.ErrExpired {
		t.Fatal(e)
	}
	if _, e = store.ConfirmStep(ctx, "alina", "alina-P2", c.ID, now, b.Tools.Location); e != actions.ErrMissing {
		t.Fatal(e)
	}
}

func TestAccessSelectionBranches(t *testing.T) {
	for _, tc := range []struct {
		name        string
		texts       []string
		cards       int
		level       string
		unavailable bool
	}{
		{"A1-A2 own team alias", []string{"Нужен доступ в пространство моей команды", "чтение"}, 1, "read", false},
		{"A2 ambiguous tracker", []string{"Нужен доступ в трекер"}, 0, "", false},
		{"A2 hidden", []string{"Нужен доступ в salary-reports"}, 0, "", false},
		{"A3 visible denial", []string{"Нужен доступ в security-incidents"}, 0, "", false},
		{"A6 multiple levels require choice", []string{"Нужен доступ в repo-payments"}, 0, "", false},
		{"A6 explicit read", []string{"Нужен доступ в repo-payments", "чтение"}, 1, "read", false},
		{"A6 unknown chooses default", []string{"Нужен доступ в repo-payments", "не знаю"}, 1, "read", false},
		{"A6 unsupported level", []string{"Нужен доступ админа в repo-payments"}, 0, "", false},
		{"A5 task continuation", []string{"Нужен доступ в launch-plans", "Всё равно оформить"}, 0, "", false},
		{"A5 decline", []string{"Нужен доступ в launch-plans", "Сначала спрошу наставника"}, 0, "", false},
		{"A7 own justification", []string{"Нужен доступ в launch-plans", "Оформить для задачи", "Для анализа запуска нового демонстрационного сервиса"}, 1, "read", false},
		{"A15 existing grant", []string{"Нужен доступ в monitoring"}, 0, "", false},
		{"A4 open request", []string{"Нужен доступ в monitoring"}, 0, "", false},
		{"A6 explicit write", []string{"Нужен доступ на запись в repo-payments"}, 1, "write", false},
		{"A4 preflight unavailable", []string{"Нужен доступ в monitoring"}, 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil).Result().Cookies()[0]
			remote.fail = tc.unavailable
			remote.granted = tc.name == "A15 existing grant"
			if tc.name == "A4 open request" {
				now := b.Tools.Clock.Now()
				remote.objects["ACC-111"] = actions.Object{ID: "ACC-111", Service: "access", UserID: "alina", ResourceID: "monitoring", Level: "read", Justification: "Для рабочей задачи", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: now, UpdatedAt: now}
			}
			for _, text := range tc.texts {
				postText(t, b, h, cookie, text)
			}
			cards, e := store.Cards(context.Background(), "alina", b.Tools.Clock.Now())
			if e != nil || len(cards) != tc.cards {
				t.Fatal(cards, e)
			}
			if tc.cards > 0 {
				var params tools.Parameters
				if json.Unmarshal(cards[0].Parameters, &params) != nil || params.Level != tc.level {
					t.Fatal(string(cards[0].Parameters))
				}
			}
			if remote.count != 0 {
				t.Fatal("preparation caused external mutation")
			}
		})
	}
}
