package api

// Тесты-доказательства ревью безопасности (output/security-review).
// Кладутся только во временную копию: backend/internal/api/secreview_api_test.go.
// Используют существующий businessFixture (одноразовая схема PostgreSQL, fakeRemote,
// подменный провайдер LLM). Настоящая LLM не вызывается.

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

const srOrigin = "http://localhost:5173"

// hostileProvider возвращает заданный JSON распознавания, имитируя враждебный
// или ошибочный ответ модели (в т.ч. после prompt injection).
type hostileProvider struct {
	agent.Fake
	reply string
}

func (p hostileProvider) Recognize(context.Context, agent.Context, string) ([]byte, error) {
	return []byte(p.reply), nil
}

func srWorker(b *Business) actions.Worker {
	return actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: b.Store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 2 * time.Second}}
}

func srAttempts(t *testing.T, b *Business, user string) int {
	t.Helper()
	var n int
	if e := b.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM attempts WHERE user_id=$1`, user).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

// R1: ответ модели не должен подтверждать действие вопреки тексту пользователя.
func TestSecReviewR1ModelCannotConfirm(t *testing.T) {
	cases := []struct{ name, text, reply string }{
		{"single", "Пока ничего не отправляй", `{"intents":[{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`},
		{"compound", "Расскажи про SPT, а карточку не трогай", `{"intents":[{"name":"smalltalk","slots":{},"confidence":1},{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`},
		{"edit-then-confirm", "Поменяй уровень, но не отправляй", `{"intents":[{"name":"card_edit","slots":{"level":"read"},"confidence":1},{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
			postText(t, b, h, cookie, "Нужен доступ в monitoring")
			cards, e := store.Cards(context.Background(), "alina", b.Tools.Clock.Now())
			if e != nil || len(cards) != 1 {
				t.Fatal(cards, e)
			}
			b.Provider = hostileProvider{reply: c.reply}
			postText(t, b, h, cookie, c.text)
			_ = srWorker(b).Once(context.Background(), store.OperationalClock.Now())
			n := srAttempts(t, b, "alina")
			t.Logf("text=%q attempts=%d external_mutations=%d", c.text, n, remote.count)
			if n != 0 || remote.count != 0 {
				t.Fatal("model output created an attempt without explicit user confirmation")
			}
		})
	}
	t.Run("step-confirm", func(t *testing.T) {
		store, b, _, h := businessFixture(t)
		ctx := context.Background()
		cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
		raw, _ := json.Marshal(map[string]any{"client_message_id": actions.ID(), "intent": "mark_done", "slots": map[string]string{"step_id": "alina-P5"}})
		bw := request(h, "POST", "/api/dialogue/actions", string(raw), srOrigin, cookie)
		var bj struct {
			Job string `json:"job_id"`
		}
		if bw.Code != 202 || json.Unmarshal(bw.Body.Bytes(), &bj) != nil {
			t.Fatal(bw.Body.String())
		}
		finishMessage(t, b, bj.Job)
		var pending int
		_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM step_confirmations WHERE step_id='alina-P5'`).Scan(&pending)
		if pending != 1 {
			t.Fatalf("precondition: no pending step confirmation (%d)", pending)
		}
		var before int64
		_ = store.Pool.QueryRow(ctx, `SELECT version FROM plan_steps WHERE id='alina-P5'`).Scan(&before)
		b.Provider = hostileProvider{reply: `{"intents":[{"name":"card_confirm","slots":{},"confidence":1}],"ambiguities":[]}`}
		postText(t, b, h, cookie, "Нет, пока не отмечай")
		var after int64
		var status string
		_ = store.Pool.QueryRow(ctx, `SELECT version,status FROM plan_steps WHERE id='alina-P5'`).Scan(&after, &status)
		t.Logf("P5 version %d -> %d status=%s", before, after, status)
		if after != before {
			t.Fatal("model output changed a manual step without explicit confirmation")
		}
	})
}

// R2: известный job не должен отдавать ответ по документу после потери прав.
// Вариант: права теряются сменой команды пользователя, а не правкой документа.
func TestSecReviewR2JobAfterTeamChange(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
	job := postText(t, b, h, cookie, "Как у нас пишут код?")
	before := request(h, "GET", "/api/jobs/"+job, "", "", cookie)
	if before.Code != 200 || !strings.Contains(before.Body.String(), "doc-pay-principles") {
		t.Skipf("precondition: answer did not cite team document: %d %s", before.Code, before.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE users SET team_id='search', policy_version=policy_version+1 WHERE id='alina'`); e != nil {
		t.Fatal(e)
	}
	after := request(h, "GET", "/api/jobs/"+job, "", "", cookie)
	var ids struct {
		ExecutionIDs []string `json:"execution_ids"`
	}
	_ = json.Unmarshal(before.Body.Bytes(), &ids)
	leak := strings.Contains(after.Body.String(), "doc-pay-principles") || strings.Contains(after.Body.String(), "Платежи")
	for _, id := range ids.ExecutionIDs {
		run := request(h, "GET", "/api/executions/"+id, "", "", cookie)
		if strings.Contains(run.Body.String(), "doc-pay-principles") || strings.Contains(run.Body.String(), "Платежи") {
			leak = true
		}
	}
	doc := request(h, "GET", "/demo/docs/doc-pay-principles", "", "", cookie)
	chat := request(h, "GET", "/api/chat", "", "", cookie)
	if doc.Code == 200 || strings.Contains(chat.Body.String(), "doc-pay-principles") {
		leak = true
	}
	t.Logf("job before=%d after=%d doc=%d leak=%v", before.Code, after.Code, doc.Code, leak)
	if leak {
		t.Fatal("document answer still exposed after the user lost access")
	}
}

// R3: попытка, задание и проекции не должны раскрывать ресурс, ставший скрытым.
func TestSecReviewR3HiddenResourceProjections(t *testing.T) {
	store, b, _, h := businessFixture(t)
	ctx := context.Background()
	cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
	body := `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`
	created := request(h, "POST", "/api/cards", body, srOrigin, cookie)
	var card struct {
		Card actions.Card `json:"card"`
	}
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &card) != nil {
		t.Fatal(created.Body.String())
	}
	w := request(h, "POST", "/api/cards/"+card.Card.ID+"/confirm", `{"expected_version":1}`, srOrigin, cookie)
	var confirmed struct {
		Attempt string `json:"attempt_id"`
		Job     string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &confirmed) != nil {
		t.Fatal(w.Body.String())
	}
	if e := srWorker(b).Once(ctx, store.OperationalClock.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE resources SET kind='hidden' WHERE id='monitoring'`); e != nil {
		t.Fatal(e)
	}
	leaks := []string{}
	for _, path := range []string{"/api/attempts/" + confirmed.Attempt, "/api/jobs/" + confirmed.Job, "/api/cards/" + card.Card.ID, "/api/onboarding", "/api/chat"} {
		r := request(h, "GET", path, "", "", cookie)
		if strings.Contains(r.Body.String(), "monitoring") || strings.Contains(r.Body.String(), "Дашборды мониторинга") {
			leaks = append(leaks, path)
		}
		t.Logf("%s -> %d", path, r.Code)
	}
	if len(leaks) > 0 {
		t.Fatalf("hidden resource exposed via %v", leaks)
	}
}

// R4: поздние версии старой заявки не должны откатывать P6 при выданном доступе.
// Вариант: approved новой заявки сохранён первым, затем доходят created и rejected старой.
func TestSecReviewR4LateOldStates(t *testing.T) {
	store, b, _, _ := businessFixture(t)
	ctx := context.Background()
	now := b.Tools.Clock.Now()
	newer := actions.Object{Service: "access", ID: "ACC-81002", UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для задач стажировки команды", ApproverID: "olga", Status: "approved", Version: 2, CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(2 * time.Hour)}
	older := newer
	older.ID, older.Status, older.Version, older.CreatedAt, older.UpdatedAt = "ACC-81001", "created", 1, now, now
	steps := []actions.Object{newer, older}
	late := older
	late.Status, late.Version, late.UpdatedAt = "rejected", 2, now.Add(3*time.Hour)
	steps = append(steps, late)
	stale := newer
	stale.Status, stale.Version, stale.UpdatedAt = "created", 1, now.Add(time.Hour)
	steps = append(steps, stale)
	for _, o := range steps {
		if e := store.ApplyObject(ctx, "alina", o, now); e != nil {
			t.Fatal(o.ID, o.Status, e)
		}
	}
	var status string
	if e := store.Pool.QueryRow(ctx, `SELECT status FROM plan_steps WHERE id='alina-P6'`).Scan(&status); e != nil {
		t.Fatal(e)
	}
	t.Logf("P6 after late old/stale states: %s", status)
	if status != "done" {
		t.Fatal("late state of another request rolled back P6")
	}
}

// Сессия: при замороженном DEMO_NOW серверный срок cookie-сессии не истекает.
// Фикстура использует бизнес-часы 2026-11-12T07:00Z и SESSION_TTL=1h, как и
// production-фабрика (api.Config.Clock = бизнес-часы, compose задаёт DEMO_NOW).
func TestSecReviewSessionNeverExpiresWithFrozenClock(t *testing.T) {
	store, _, _, h := businessFixture(t)
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil)
	cookie := login.Result().Cookies()[0]
	var expires time.Time
	if e := store.Pool.QueryRow(context.Background(), `SELECT max(expires_at) FROM sessions WHERE user_id='alina'`).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	me := request(h, "GET", "/api/me", "", "", cookie)
	wall := time.Now().UTC()
	t.Logf("session expires_at=%s wall_clock=%s expired_by=%s /api/me=%d", expires.UTC().Format(time.RFC3339), wall.Format(time.RFC3339), wall.Sub(expires).Round(time.Minute), me.Code)
	if wall.After(expires) && me.Code == 200 {
		t.Fatal("session accepted after its server-side expiry: expiry is measured by frozen business clock")
	}
}

// Ответ модели может выставить slots.query = reapplyQuery и пропустить шаг
// явного согласия на повторную подачу после отказа.
func TestSecReviewModelSkipsReapplyConsent(t *testing.T) {
	for _, withQuery := range []bool{false, true} {
		name := map[bool]string{false: "control", true: "model-query"}[withQuery]
		t.Run(name, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			now := b.Tools.Clock.Now()
			reason := "Демонстрационный отказ"
			remote.objects["ABS-9001"] = actions.Object{Service: "time_off", ID: "ABS-9001", UserID: "alina", StartDate: "2026-11-16", EndDate: "2026-11-16", Workdays: 1, ApproverID: "olga", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
			query := ""
			if withQuery {
				query = reapplyQuery
			}
			reply, _ := json.Marshal(agent.Intents{Intents: []agent.Intent{{Name: "time_off_request", Slots: agent.Slots{StartDate: "2026-11-16", EndDate: "2026-11-16", Query: query}, Confidence: 1}}, Ambiguities: []string{}})
			b.Provider = hostileProvider{reply: string(reply)}
			postText(t, b, h, cookie, "Нужен отгул 16 ноября")
			cards, e := store.Cards(context.Background(), "alina", now)
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("model query=%q -> cards=%d", query, len(cards))
			if withQuery && len(cards) != 0 {
				t.Fatal("model-provided slot bypassed the explicit reapply consent step")
			}
		})
	}
}

// Контроль: двойное/параллельное подтверждение одной версии даёт одну попытку
// и один внешний эффект; чужие идентификаторы не раскрываются (IDOR).
func TestSecReviewConcurrencyAndIDOR(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	alina := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, srOrigin, nil).Result().Cookies()[0]
	maxim := request(h, "POST", "/api/demo/session", `{"user_id":"maxim"}`, srOrigin, nil).Result().Cookies()[0]
	igor := request(h, "POST", "/api/demo/session", `{"user_id":"igor"}`, srOrigin, nil).Result().Cookies()[0]
	body := `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`
	var card struct {
		Card actions.Card `json:"card"`
	}
	_ = json.Unmarshal(request(h, "POST", "/api/cards", body, srOrigin, alina).Body.Bytes(), &card)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	codes := make([]int, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(h, "POST", "/api/cards/"+card.Card.ID+"/confirm", `{"expected_version":1}`, srOrigin, alina)
			var out struct {
				ID string `json:"attempt_id"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			ids[i], codes[i] = out.ID, w.Code
		}(i)
	}
	wg.Wait()
	for i := 0; i < 4; i++ {
		_ = srWorker(b).Once(ctx, store.OperationalClock.Now())
	}
	distinct := map[string]bool{}
	for _, id := range ids {
		distinct[id] = true
	}
	t.Logf("codes=%v distinct_attempts=%d external_mutations=%d", codes, len(distinct), remote.count)
	if len(distinct) != 1 || remote.count != 1 {
		t.Fatal("parallel confirmation produced more than one effect")
	}
	var job string
	_ = store.Pool.QueryRow(ctx, `SELECT id::text FROM jobs WHERE attempt_id=$1`, ids[0]).Scan(&job)
	var run string
	_ = store.Pool.QueryRow(ctx, `SELECT run_id::text FROM execution_links WHERE user_id='alina' LIMIT 1`).Scan(&run)
	probes := []struct{ method, path, body string }{
		{"GET", "/api/cards/" + card.Card.ID, ""},
		{"GET", "/api/attempts/" + ids[0], ""},
		{"GET", "/api/jobs/" + job, ""},
		{"GET", "/api/executions/" + run, ""},
		{"POST", "/api/attempts/" + ids[0] + "/reconcile", `{}`},
		{"POST", "/api/attempts/" + ids[0] + "/retry", `{}`},
		{"POST", "/api/cards/" + card.Card.ID + "/discard", `{"expected_version":1}`},
		{"POST", "/api/requests/refresh", `{"kind":"access","id":"ACC-5001"}`},
	}
	for _, c := range []struct {
		name string
		ck   int
	}{{"maxim", 0}, {"igor", 1}} {
		ck := maxim
		if c.ck == 1 {
			ck = igor
		}
		for _, p := range probes {
			origin := ""
			if p.method != "GET" {
				origin = srOrigin
			}
			w := request(h, p.method, p.path, p.body, origin, ck)
			t.Logf("%s %s %s -> %d", c.name, p.method, p.path, w.Code)
			if w.Code < 400 || strings.Contains(w.Body.String(), "monitoring") || strings.Contains(w.Body.String(), "alina") {
				t.Fatalf("%s reached alina's object via %s %s: %d %s", c.name, p.method, p.path, w.Code, w.Body.String())
			}
		}
	}
	plan := request(h, "GET", "/api/onboarding", "", "", igor)
	chat := request(h, "GET", "/api/chat", "", "", igor)
	if strings.Contains(plan.Body.String(), "ACC-") || strings.Contains(chat.Body.String(), "monitoring") {
		t.Fatal("buddy sees intern requests or chat")
	}
	// Подмена user_id и роли в теле не меняет идентичность сессии.
	spoof := request(h, "POST", "/api/cards", `{"type":"access_create","user_id":"alina","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`, srOrigin, igor)
	t.Logf("spoofed user_id -> %d", spoof.Code)
	if spoof.Code < 400 {
		t.Fatal("body user_id accepted")
	}
	// Мутация без Origin и с чужим Origin отклоняется (CSRF).
	for _, o := range []string{"", "https://evil.example"} {
		w := request(h, "POST", "/api/cards/"+card.Card.ID+"/discard", `{"expected_version":1}`, o, alina)
		t.Logf("origin=%q -> %d", o, w.Code)
		if w.Code != 403 {
			t.Fatal("mutation accepted without allowed Origin")
		}
	}
}
