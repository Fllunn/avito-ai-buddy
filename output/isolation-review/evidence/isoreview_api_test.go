package api

// Тесты-доказательства ревью изоляции данных (output/isolation-review).
// Кладутся только во временную копию: backend/internal/api/isoreview_api_test.go.
// Используют существующий businessFixture: одноразовая схема PostgreSQL,
// fakeRemote вместо HTTP-имитаций и подменный провайдер LLM, который
// записывает всё, что ему передали. Настоящая LLM не вызывается.

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const isoOrigin = "http://localhost:5173"

// ---------- подменный провайдер, записывающий контекст модели ----------

type isoCall struct {
	User, Method, Query, Frame string
}

type recordingProvider struct {
	agent.Fake
	mu    *sync.Mutex
	calls *[]isoCall
}

func newRecorder() recordingProvider {
	return recordingProvider{mu: &sync.Mutex{}, calls: &[]isoCall{}}
}
func (p recordingProvider) record(method string, frame agent.Context, query string) {
	raw, _ := json.Marshal(frame)
	p.mu.Lock()
	*p.calls = append(*p.calls, isoCall{frame.Profile.ID, method, query, string(raw)})
	p.mu.Unlock()
}
func (p recordingProvider) Recognize(ctx context.Context, frame agent.Context, query string) ([]byte, error) {
	p.record("Recognize", frame, query)
	return p.Fake.Recognize(ctx, frame, query)
}
func (p recordingProvider) SelectFragments(ctx context.Context, frame agent.Context, query string) ([]byte, error) {
	p.record("SelectFragments", frame, query)
	return p.Fake.SelectFragments(ctx, frame, query)
}
func (p recordingProvider) snapshot() []isoCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]isoCall(nil), (*p.calls)...)
}

// ---------- помощники ----------

func isoLogin(t *testing.T, h http.Handler, user string, previous *http.Cookie) *http.Cookie {
	t.Helper()
	w := request(h, "POST", "/api/demo/session", `{"user_id":"`+user+`"}`, isoOrigin, previous)
	if w.Code != 200 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login %s: %d %s", user, w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}

func isoPostText(t *testing.T, h http.Handler, ck *http.Cookie, client, text string) (int, string, string) {
	raw, _ := json.Marshal(map[string]string{"client_message_id": client, "text": text})
	w := request(h, "POST", "/api/chat/messages", string(raw), isoOrigin, ck)
	var out struct {
		Message int64  `json:"message_id"`
		Job     string `json:"job_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, strconv.FormatInt(out.Message, 10), out.Job
}

func isoWorker(b *Business) actions.Worker {
	return actions.Worker{BusinessClock: b.Tools.Clock, Location: b.Tools.Location, Store: b.Store, Client: b.Tools, Timeout: time.Second, Lease: time.Minute, Delays: []time.Duration{time.Second, 2 * time.Second}}
}

// isoDrain обрабатывает очередь сообщений, refresh и действий несколькими
// параллельными обработчиками, пока работа не закончится.
func isoDrain(t *testing.T, b *Business, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 60; round++ {
		var progressed atomic.Int32
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := b.ProcessOnce(ctx, now); e == nil {
					progressed.Add(1)
				} else if !errors.Is(e, actions.ErrNoWork) && !errors.Is(e, actions.ErrLeaseLost) {
					errs <- e
				}
				if e := isoWorker(b).Once(ctx, now); e == nil {
					progressed.Add(1)
				} else if !errors.Is(e, actions.ErrNoWork) && !errors.Is(e, actions.ErrLeaseLost) {
					errs <- e
				}
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Errorf("background processing error: %v", e)
		}
		if progressed.Load() == 0 {
			return
		}
	}
	t.Fatal("queue did not drain")
}

type isoChat struct {
	Items []struct {
		ID       int64           `json:"id"`
		Sequence int64           `json:"sequence"`
		Role     string          `json:"role"`
		Text     string          `json:"text"`
		Metadata json.RawMessage `json:"metadata"`
	} `json:"items"`
	Next  int64          `json:"next_cursor"`
	Cards []actions.Card `json:"active_cards"`
}

func isoGetChat(t *testing.T, h http.Handler, ck *http.Cookie, query string) (isoChat, string) {
	t.Helper()
	w := request(h, "GET", "/api/chat"+query, "", "", ck)
	var c isoChat
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatalf("chat: %d %s", w.Code, w.Body.String())
	}
	return c, w.Body.String()
}

func isoCreateCard(t *testing.T, h http.Handler, ck *http.Cookie, body string) actions.Card {
	t.Helper()
	w := request(h, "POST", "/api/cards", body, isoOrigin, ck)
	var out struct {
		Card actions.Card `json:"card"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("card: %d %s", w.Code, w.Body.String())
	}
	return out.Card
}

type isoConfirmed struct {
	Attempt string   `json:"attempt_id"`
	Job     string   `json:"job_id"`
	Routes  []string `json:"execution_ids"`
}

func isoConfirm(t *testing.T, h http.Handler, ck *http.Cookie, c actions.Card) isoConfirmed {
	t.Helper()
	w := request(h, "POST", "/api/cards/"+c.ID+"/confirm", `{"expected_version":`+strconv.Itoa(c.Version)+`}`, isoOrigin, ck)
	var out isoConfirmed
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	return out
}

var requestIDPattern = regexp.MustCompile(`"request_id":"[^"]*"`)

// normalizedError убирает случайный request_id, чтобы сравнить тела ошибок.
func normalizedError(body string) string {
	return requestIDPattern.ReplaceAllString(strings.TrimSpace(body), `"request_id":"*"`)
}

func objectIDs(remote *fakeRemote, user string) []string {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	ids := []string{}
	for id, o := range remote.objects {
		if o.UserID == user {
			ids = append(ids, id)
		}
	}
	return ids
}

func executionIDs(t *testing.T, h http.Handler, ck *http.Cookie) []string {
	t.Helper()
	w := request(h, "GET", "/api/executions?limit=100", "", "", ck)
	var list struct {
		Items []struct {
			ID        string    `json:"id"`
			StartedAt time.Time `json:"started_at"`
		} `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("executions: %d %s", w.Code, w.Body.String())
	}
	ids := []string{}
	for _, item := range list.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// ---------- общий сценарий: Алина и Максим работают одновременно ----------

type isoWorld struct {
	b        *Business
	remote   *fakeRemote
	h        http.Handler
	rec      recordingProvider
	now      time.Time
	alina    *http.Cookie
	maxim    *http.Cookie
	cards    map[string][]actions.Card
	confirms map[string][]isoConfirmed
	jobs     map[string][]string
	messages map[string][]string
}

const (
	alinaMarker = "МАРКЕРАЛИНА77"
	maximMarker = "МАРКЕРМАКСИМ42"
)

func isoScenario(t *testing.T) *isoWorld {
	t.Helper()
	store, b, remote, h := businessFixture(t)
	rec := newRecorder()
	b.Provider = rec
	world := &isoWorld{b: b, remote: remote, h: h, rec: rec, now: store.OperationalClock.Now(), cards: map[string][]actions.Card{}, confirms: map[string][]isoConfirmed{}, jobs: map[string][]string{}, messages: map[string][]string{}}
	world.alina = isoLogin(t, h, "alina", nil)
	world.maxim = isoLogin(t, h, "maxim", nil)
	type script struct {
		user     string
		ck       *http.Cookie
		marker   string
		card     string
		question string
	}
	scripts := []script{
		{"alina", world.alina, alinaMarker, `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки в платежах"}}`, "Как у нас пишут код?"},
		{"maxim", world.maxim, maximMarker, `{"type":"access_create","parameters":{"resource_id":"space-search","level":"read","justification":"Для задач стажировки в поиске"}}`, "Где инструкция по библиотеке компонентов?"},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range scripts {
		wg.Add(1)
		go func(s script) {
			defer wg.Done()
			texts := []string{"Привет, спасибо! " + s.marker, s.question, "Нужен доступ к мониторингу", "Что дальше?"}
			for _, text := range texts {
				code, id, job := isoPostText(t, h, s.ck, actions.ID(), text)
				if code != 202 {
					t.Errorf("%s post %q: %d", s.user, text, code)
					continue
				}
				mu.Lock()
				world.messages[s.user] = append(world.messages[s.user], id)
				world.jobs[s.user] = append(world.jobs[s.user], job)
				mu.Unlock()
			}
			w := request(h, "POST", "/api/cards", s.card, isoOrigin, s.ck)
			var out struct {
				Card actions.Card `json:"card"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Errorf("%s card: %d %s", s.user, w.Code, w.Body.String())
				return
			}
			mu.Lock()
			world.cards[s.user] = append(world.cards[s.user], out.Card)
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	isoDrain(t, b, world.now)
	// Подтверждаем все черновики обоих одновременно (включая карточку из чата).
	wg = sync.WaitGroup{}
	for _, s := range scripts {
		chat, _ := isoGetChat(t, h, s.ck, "")
		for _, c := range chat.Cards {
			if c.State != "draft" {
				continue
			}
			wg.Add(1)
			go func(user string, ck *http.Cookie, c actions.Card) {
				defer wg.Done()
				w := request(h, "POST", "/api/cards/"+c.ID+"/confirm", `{"expected_version":`+strconv.Itoa(c.Version)+`}`, isoOrigin, ck)
				var out isoConfirmed
				if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
					t.Errorf("%s confirm: %d %s", user, w.Code, w.Body.String())
					return
				}
				mu.Lock()
				world.confirms[user] = append(world.confirms[user], out)
				world.cards[user] = append(world.cards[user], c)
				mu.Unlock()
			}(s.user, s.ck, c)
		}
	}
	wg.Wait()
	isoDrain(t, b, world.now)
	// Второе дело каждого — отгул через форму, подтверждения снова одновременно.
	wg = sync.WaitGroup{}
	for _, s := range []struct {
		user, day string
		ck        *http.Cookie
	}{{"alina", "2026-11-16", world.alina}, {"maxim", "2026-11-17", world.maxim}} {
		wg.Add(1)
		go func(user, day string, ck *http.Cookie) {
			defer wg.Done()
			w := request(h, "POST", "/api/cards", `{"type":"time_off_create","parameters":{"start_date":"`+day+`","end_date":"`+day+`"}}`, isoOrigin, ck)
			var out struct {
				Card actions.Card `json:"card"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
				t.Errorf("%s time off card: %d %s", user, w.Code, w.Body.String())
				return
			}
			c := request(h, "POST", "/api/cards/"+out.Card.ID+"/confirm", `{"expected_version":1}`, isoOrigin, ck)
			var conf isoConfirmed
			if c.Code != 202 || json.Unmarshal(c.Body.Bytes(), &conf) != nil {
				t.Errorf("%s time off confirm: %d %s", user, c.Code, c.Body.String())
				return
			}
			mu.Lock()
			world.cards[user] = append(world.cards[user], out.Card)
			world.confirms[user] = append(world.confirms[user], conf)
			mu.Unlock()
		}(s.user, s.day, s.ck)
	}
	wg.Wait()
	isoDrain(t, b, world.now)
	// Проверка статусов обоими одновременно (refresh-задания).
	for _, ck := range []*http.Cookie{world.alina, world.maxim} {
		if w := request(h, "POST", "/api/requests/refresh", `{"kind":"access"}`, isoOrigin, ck); w.Code != 202 {
			t.Fatalf("refresh: %d", w.Code)
		}
	}
	isoDrain(t, b, world.now)
	if len(world.confirms["alina"]) < 2 || len(world.confirms["maxim"]) < 2 {
		t.Fatalf("scenario did not produce two confirmed actions per user: %v", world.confirms)
	}
	return world
}

func foreignMarkers(world *isoWorld, user string) []string {
	if user == "alina" {
		return append([]string{maximMarker, "maxim-", "Максим", "doc-srch", "Для задач стажировки в поиске"}, objectIDs(world.remote, "maxim")...)
	}
	return append([]string{alinaMarker, "alina-", "Алина", "doc-pay", "Для задач стажировки в платежах"}, objectIDs(world.remote, "alina")...)
}

// П.1: параллельная работа двух стажёров — каждый видит только своё.
func TestIsoParallelInternsSeeOnlyOwnData(t *testing.T) {
	world := isoScenario(t)
	h := world.h
	for _, u := range []struct {
		name string
		own  *http.Cookie
	}{{"alina", world.alina}, {"maxim", world.maxim}} {
		bad := foreignMarkers(world, u.name)
		_, chat := isoGetChat(t, h, u.own, "")
		plan := request(h, "GET", "/api/onboarding", "", "", u.own).Body.String()
		execs := request(h, "GET", "/api/executions?limit=100", "", "", u.own).Body.String()
		for _, surface := range []struct{ name, body string }{{"chat", chat}, {"onboarding", plan}, {"executions", execs}} {
			for _, m := range bad {
				if strings.Contains(surface.body, m) {
					t.Errorf("%s sees foreign marker %q in %s", u.name, m, surface.name)
				}
			}
		}
		own := alinaMarker
		if u.name == "maxim" {
			own = maximMarker
		}
		if !strings.Contains(chat, own) {
			t.Errorf("%s does not see own marker", u.name)
		}
		for _, c := range world.cards[u.name] {
			if w := request(h, "GET", "/api/cards/"+c.ID, "", "", u.own); w.Code != 200 {
				t.Errorf("%s own card %d", u.name, w.Code)
			}
		}
	}
	t.Logf("alina objects=%v maxim objects=%v", objectIDs(world.remote, "alina"), objectIDs(world.remote, "maxim"))
	// Каждая чужая сущность — 404 с тем же телом, что у несуществующей.
	missing := normalizedError(request(h, "GET", "/api/cards/"+actions.ID(), "", "", world.maxim).Body.String())
	probe := func(owner, other string, ck *http.Cookie, kind, path string) {
		w := request(h, "GET", path, "", "", ck)
		body := normalizedError(w.Body.String())
		if w.Code != 404 || body != missing {
			t.Errorf("%s reads %s's %s: %d %s", other, owner, kind, w.Code, body)
		}
	}
	for _, pair := range [][2]string{{"alina", "maxim"}, {"maxim", "alina"}} {
		owner, other := pair[0], pair[1]
		ck := world.maxim
		if other == "alina" {
			ck = world.alina
		}
		for _, c := range world.cards[owner] {
			probe(owner, other, ck, "card", "/api/cards/"+c.ID)
		}
		for _, c := range world.confirms[owner] {
			probe(owner, other, ck, "attempt", "/api/attempts/"+c.Attempt)
			probe(owner, other, ck, "job", "/api/jobs/"+c.Job)
			for _, r := range c.Routes {
				w := request(h, "GET", "/api/executions/"+r+"?after_sequence=0", "", "", ck)
				if w.Code != 404 || strings.Contains(w.Body.String(), owner) {
					t.Errorf("%s reads %s's route: %d", other, owner, w.Code)
				}
			}
		}
		for _, j := range world.jobs[owner] {
			probe(owner, other, ck, "message job", "/api/jobs/"+j)
		}
	}
	// Множества маршрутов не пересекаются.
	a, m := executionIDs(t, h, world.alina), executionIDs(t, h, world.maxim)
	seen := map[string]bool{}
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range m {
		if seen[id] {
			t.Errorf("route %s listed for both users", id)
		}
	}
	t.Logf("routes alina=%d maxim=%d", len(a), len(m))
	// Курсор маршрутов, собранный из позиции Алины, не выводит Максима на её записи.
	var newest struct {
		Items []struct {
			ID        string    `json:"id"`
			StartedAt time.Time `json:"started_at"`
		} `json:"items"`
	}
	_ = json.Unmarshal(request(h, "GET", "/api/executions?limit=1", "", "", world.alina).Body.Bytes(), &newest)
	if len(newest.Items) == 1 {
		future, _ := json.Marshal(map[string]any{"at": newest.Items[0].StartedAt.Add(time.Hour), "id": "ffffffff-ffff-4fff-bfff-ffffffffffff"})
		cursor := base64.RawURLEncoding.EncodeToString(future)
		w := request(h, "GET", "/api/executions?limit=100&cursor="+cursor, "", "", world.maxim)
		for _, id := range a {
			if strings.Contains(w.Body.String(), id) {
				t.Errorf("crafted cursor exposes alina route %s", id)
			}
		}
	}
	// Курсор чата: последовательности личные, after не выходит на чужую ленту.
	chatA, _ := isoGetChat(t, h, world.alina, "")
	for _, after := range []string{"?after=0", "?after=1", "?after=" + strconv.FormatInt(chatA.Next, 10)} {
		_, body := isoGetChat(t, h, world.maxim, after)
		if strings.Contains(body, alinaMarker) {
			t.Errorf("chat cursor %s exposes alina", after)
		}
	}
}

// П.2: контекст LLM запроса Алины не содержит данных Максима (и наоборот).
func TestIsoLLMContextContainsOnlyOwnData(t *testing.T) {
	world := isoScenario(t)
	// Наставник тоже пишет: его контекст — только назначенные ему шаги.
	igor := isoLogin(t, world.h, "igor", nil)
	if code, _, _ := isoPostText(t, world.h, igor, actions.ID(), "Что дальше? Как у нас пишут код?"); code != 202 {
		t.Fatal(code)
	}
	isoDrain(t, world.b, world.now)
	calls := world.rec.snapshot()
	counts := map[string]int{}
	for _, c := range calls {
		counts[c.User+":"+c.Method]++
		var bad []string
		switch c.User {
		case "alina":
			bad = append([]string{maximMarker, "maxim", "Максим", "doc-srch", "sergey"}, objectIDs(world.remote, "maxim")...)
		case "maxim":
			bad = append([]string{alinaMarker, "alina", "Алина", "doc-pay", "igor"}, objectIDs(world.remote, "alina")...)
		case "igor":
			bad = append([]string{alinaMarker, maximMarker, "maxim", "doc-srch", "ACC-", "Для задач стажировки"}, objectIDs(world.remote, "alina")...)
		default:
			t.Errorf("unexpected provider call for %q", c.User)
		}
		for _, m := range bad {
			if strings.Contains(c.Frame, m) || strings.Contains(c.Query, m) {
				t.Errorf("%s %s context contains foreign %q", c.User, c.Method, m)
			}
		}
		var frame agent.Context
		if json.Unmarshal([]byte(c.Frame), &frame) != nil {
			t.Fatal("frame is not JSON")
		}
		for _, s := range frame.Steps {
			if c.User != "igor" && s.InternID != c.User {
				t.Errorf("%s context has foreign step %s", c.User, s.ID)
			}
			if c.User == "igor" && s.OwnerID != "igor" {
				t.Errorf("igor context has unassigned step %s", s.ID)
			}
		}
		for _, d := range frame.Documents {
			if d.TeamID != nil && frame.Profile.TeamID != nil && *d.TeamID != *frame.Profile.TeamID {
				t.Errorf("%s context has foreign team document %s", c.User, d.ID)
			}
		}
	}
	t.Logf("provider calls: %v", counts)
	for _, need := range []string{"alina:Recognize", "alina:SelectFragments", "maxim:Recognize", "maxim:SelectFragments", "igor:Recognize"} {
		if counts[need] == 0 {
			t.Errorf("no recorded %s call", need)
		}
	}
}

// П.3: фоновые обработчики — потеря lease, перезапуск, dialogue_state, OfferNext.
func TestIsoBackgroundLeaseRestartAndDialogueState(t *testing.T) {
	store, b, _, h := businessFixture(t)
	rec := newRecorder()
	b.Provider = rec
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina, maxim := isoLogin(t, h, "alina", nil), isoLogin(t, h, "maxim", nil)
	isoPostText(t, h, alina, actions.ID(), "Привет "+alinaMarker)
	isoPostText(t, h, maxim, actions.ID(), "Привет "+maximMarker)
	isoPostText(t, h, alina, actions.ID(), "Нужен доступ к мониторингу")
	isoPostText(t, h, maxim, actions.ID(), "Нужен доступ к мониторингу")
	// Старый обработчик взял задание и «завис»: lease 1 с.
	stale, e := store.ClaimMessage(ctx, now, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	// После перезапуска новый процесс видит истёкший lease и обрабатывает всё.
	isoDrain(t, b, now.Add(5*time.Second))
	// Поздний ответ старого обработчика отклоняется и ничего не пишет.
	before := map[string]int{}
	for _, u := range []string{"alina", "maxim"} {
		items, _ := store.Messages(ctx, u, 0)
		before[u] = len(items)
	}
	err := store.CompleteMessage(ctx, stale, alinaSafeText(), map[string]any{"items": []any{}}, "", now, json.RawMessage(`{"stage":"confirm_card","card_id":"`+actions.ID()+`"}`))
	if !errors.Is(err, actions.ErrLeaseLost) {
		t.Errorf("stale worker completion: %v", err)
	}
	for _, u := range []string{"alina", "maxim"} {
		items, _ := store.Messages(ctx, u, 0)
		if len(items) != before[u] {
			t.Errorf("stale worker changed %s history", u)
		}
	}
	// Ответы попали в «свои» ленты; dialogue_state ссылается только на свои карточки.
	for _, u := range []struct {
		name, own, foreign string
		ck                 *http.Cookie
	}{{"alina", alinaMarker, maximMarker, alina}, {"maxim", maximMarker, alinaMarker, maxim}} {
		_, body := isoGetChat(t, h, u.ck, "")
		if !strings.Contains(body, u.own) || strings.Contains(body, u.foreign) {
			t.Errorf("%s history mixed", u.name)
		}
		raw, _ := store.Dialogue(ctx, u.name)
		var state DialogueState
		_ = json.Unmarshal(raw, &state)
		if state.CardID != "" {
			var owner string
			_ = store.Pool.QueryRow(ctx, `SELECT user_id FROM cards WHERE id=$1`, state.CardID).Scan(&owner)
			if owner != u.name {
				t.Errorf("%s dialogue_state points to %s's card", u.name, owner)
			}
		}
		if state.ExecutionID != "" {
			var owner string
			_ = store.Pool.QueryRow(ctx, `SELECT user_id FROM execution_runs WHERE id=$1`, state.ExecutionID).Scan(&owner)
			if owner != u.name {
				t.Errorf("%s dialogue_state points to %s's route", u.name, owner)
			}
		}
		t.Logf("%s dialogue stage=%q card=%v", u.name, state.Stage, state.CardID != "")
	}
	// Каждое задание и ответ принадлежат автору исходного сообщения.
	var mixed int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs j JOIN messages m ON m.id=j.message_id WHERE j.user_id<>m.user_id`).Scan(&mixed)
	if mixed != 0 {
		t.Errorf("%d jobs belong to another user's message", mixed)
	}
	// Worker действий: потеря lease у одного пользователя не трогает другого.
	ca := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки в платежах"}}`)
	cm := isoCreateCard(t, h, maxim, `{"type":"access_create","parameters":{"resource_id":"space-search","level":"read","justification":"Для задач стажировки в поиске"}}`)
	aa, am := isoConfirm(t, h, alina, ca), isoConfirm(t, h, maxim, cm)
	work, e := store.Claim(ctx, now.Add(10*time.Second), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	isoDrain(t, b, now.Add(20*time.Second))
	if e := store.MarkSending(ctx, work, now.Add(20*time.Second)); !errors.Is(e, actions.ErrLeaseLost) {
		t.Errorf("stale action worker write: %v", e)
	}
	for _, c := range []struct {
		user, attempt string
		ck            *http.Cookie
	}{{"alina", aa.Attempt, alina}, {"maxim", am.Attempt, maxim}} {
		w := request(h, "GET", "/api/attempts/"+c.attempt, "", "", c.ck)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "succeeded") {
			t.Errorf("%s attempt after lease loss: %d %s", c.user, w.Code, w.Body.String())
		}
	}
	var foreignObjects int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM service_objects WHERE user_id<>payload->>'user_id'`).Scan(&foreignObjects)
	if foreignObjects != 0 {
		t.Errorf("%d service objects stored under another user", foreignObjects)
	}
	// OfferNext: приглашение к следующему делу получает только владелец состояния.
	discard := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`)
	if w := request(h, "POST", "/api/cards/"+discard.ID+"/discard", `{"expected_version":1}`, isoOrigin, alina); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE dialogue_state SET payload=jsonb_build_object('card_id',$2::text,'pending',jsonb_build_array(jsonb_build_object('name','time_off_request')),'stage','waiting_result') WHERE user_id=$1`, "alina", discard.ID); e != nil {
		t.Fatal(e)
	}
	maximBefore, _ := store.Messages(ctx, "maxim", 0)
	if offered, e := store.OfferNext(ctx, now); e != nil || !offered {
		t.Fatalf("offer: %v %v", offered, e)
	}
	_, alinaChat := isoGetChat(t, h, alina, "")
	maximAfter, _ := store.Messages(ctx, "maxim", 0)
	if !strings.Contains(alinaChat, "Перейдём к отгулу") || len(maximAfter) != len(maximBefore) {
		t.Errorf("OfferNext reached the wrong user")
	}
}

// П.4: коллизии идентификаторов и чужие ID в path, query и body.
func TestIsoIdentifierCollisions(t *testing.T) {
	store, b, remote, h := businessFixture(t)
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina, maxim := isoLogin(t, h, "alina", nil), isoLogin(t, h, "maxim", nil)
	shared := actions.ID()
	ca, ia, ja := isoPostText(t, h, alina, shared, "Привет "+alinaMarker)
	cm, im, jm := isoPostText(t, h, maxim, shared, "Привет "+maximMarker)
	t.Logf("same client_message_id: alina=%d/%s maxim=%d/%s", ca, ia, cm, im)
	// message_id личный (IR-03 исправлен): у двух пользователей может совпадать; задания разные.
	if ca != 202 || cm != 202 || ja == jm {
		t.Fatalf("shared client id across users: %d %d %s %s", ca, cm, ia, im)
	}
	if c, id, _ := isoPostText(t, h, maxim, shared, "Привет "+maximMarker); c != 202 || id != im {
		t.Errorf("own replay: %d %s", c, id)
	}
	if c, _, _ := isoPostText(t, h, maxim, shared, "Другой текст"); c != 409 {
		t.Errorf("own conflicting replay: %d", c)
	}
	if c, _, _ := isoPostText(t, h, alina, shared, "Привет "+maximMarker); c != 409 {
		t.Errorf("alina with maxim's text under her own id: %d", c)
	}
	button, _ := json.Marshal(map[string]any{"client_message_id": shared, "intent": "next_step", "slots": map[string]string{}})
	if w := request(h, "POST", "/api/dialogue/actions", string(button), isoOrigin, maxim); w.Code != 409 {
		t.Errorf("button reusing own text id: %d", w.Code)
	}
	isoDrain(t, b, now)
	_, achat := isoGetChat(t, h, alina, "")
	if strings.Contains(achat, maximMarker) || !strings.Contains(achat, alinaMarker) {
		t.Error("alina's message replaced or mixed by shared client id")
	}
	// Объекты Алины: карточка, попытка, маршрут, ACC и ABS, подтверждение шага.
	card := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки в платежах"}}`)
	conf := isoConfirm(t, h, alina, card)
	absence := isoCreateCard(t, h, alina, `{"type":"time_off_create","parameters":{"start_date":"2026-11-16","end_date":"2026-11-16"}}`)
	isoConfirm(t, h, alina, absence)
	isoDrain(t, b, now)
	var acc, abs string
	for _, id := range objectIDs(remote, "alina") {
		if strings.HasPrefix(id, "ACC-") {
			acc = id
		} else {
			abs = id
		}
	}
	if acc == "" || abs == "" {
		t.Fatalf("objects: %v", objectIDs(remote, "alina"))
	}
	draft := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`)
	stepConf := request(h, "POST", "/api/steps/alina-P5/confirmation", `{"expected_version":1,"target_state":"done"}`, isoOrigin, alina)
	var sc struct {
		ID string `json:"confirmation_id"`
	}
	if stepConf.Code != 200 || json.Unmarshal(stepConf.Body.Bytes(), &sc) != nil {
		t.Fatal(stepConf.Body.String())
	}
	missing := normalizedError(request(h, "GET", "/api/cards/"+actions.ID(), "", "", maxim).Body.String())
	route := ""
	if len(conf.Routes) > 0 {
		route = conf.Routes[0]
	}
	probes := []struct{ method, path, body string }{
		{"GET", "/api/cards/" + draft.ID, ""},
		{"PATCH", "/api/cards/" + draft.ID, `{"expected_version":1,"changes":{"level":"read"}}`},
		{"POST", "/api/cards/" + draft.ID + "/confirm", `{"expected_version":1}`},
		{"POST", "/api/cards/" + draft.ID + "/discard", `{"expected_version":1}`},
		{"GET", "/api/attempts/" + conf.Attempt, ""},
		{"POST", "/api/attempts/" + conf.Attempt + "/reconcile", `{}`},
		{"POST", "/api/attempts/" + conf.Attempt + "/retry", `{}`},
		{"GET", "/api/jobs/" + conf.Job, ""},
		{"GET", "/api/jobs/" + ja, ""},
		{"GET", "/api/executions/" + route, ""},
		{"POST", "/api/steps/alina-P5/confirmation", `{"expected_version":1,"target_state":"done"}`},
		{"POST", "/api/steps/alina-P5/confirm", `{"confirmation_id":"` + sc.ID + `"}`},
		{"POST", "/api/steps/maxim-P9/confirm", `{"confirmation_id":"` + sc.ID + `"}`},
		{"POST", "/api/requests/refresh", `{"kind":"access","id":"` + acc + `"}`},
		{"POST", "/api/requests/refresh", `{"kind":"time_off","id":"` + abs + `"}`},
		{"POST", "/api/cards", `{"type":"access_cancel","parameters":{"request_id":"` + acc + `","expected_version":1}}`},
		{"POST", "/api/cards", `{"type":"time_off_cancel","parameters":{"request_id":"` + abs + `","expected_version":1}}`},
	}
	for _, p := range probes {
		origin := ""
		if p.method != "GET" {
			origin = isoOrigin
		}
		w := request(h, p.method, p.path, p.body, origin, maxim)
		body := w.Body.String()
		t.Logf("maxim %s %s -> %d %s", p.method, p.path, w.Code, normalizedError(body))
		if w.Code < 400 || w.Code == 403 && !strings.Contains(p.path, "retry") || strings.Contains(body, "alina") || strings.Contains(body, "space-payments") || strings.Contains(body, acc) && !strings.Contains(p.body, acc) {
			t.Errorf("maxim reached alina's object: %s %s -> %d %s", p.method, p.path, w.Code, body)
		}
		if w.Code == 404 && normalizedError(body) != missing {
			t.Errorf("404 body differs from unknown object for %s: %s", p.path, body)
		}
	}
	// Чужие номера в слотах кнопок: отмена и перенос не готовятся.
	postButton(t, b, h, maxim, "maxim", "cancel", map[string]string{"request_id": acc})
	postButton(t, b, h, maxim, "maxim", "time_off_request", map[string]string{"request_id": abs, "start_date": "2026-11-17", "end_date": "2026-11-17"})
	postButton(t, b, h, maxim, "maxim", "mark_done", map[string]string{"step_id": "alina-P5"})
	mitems, _ := isoGetChat(t, h, maxim, "")
	var maximCards int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE user_id='maxim'`).Scan(&maximCards)
	if maximCards != 0 {
		t.Errorf("foreign slot ids produced %d maxim cards", maximCards)
	}
	// Собственный ввод Максима (слоты кнопки) может содержать набранный им номер;
	// ответы помощника не должны добавлять сведений об объекте Алины.
	for _, item := range mitems.Items {
		if item.Role != "assistant" {
			continue
		}
		t.Logf("assistant: %q", item.Text)
		if strings.Contains(item.Text, "2026-11-16") || strings.Contains(item.Text, "space-payments") || strings.Contains(string(item.Metadata), "space-payments") || strings.Contains(string(item.Metadata), "2026-11-16") || strings.Contains(item.Text, "Завести конспект") {
			t.Errorf("assistant reply leaks alina's object: %q %s", item.Text, item.Metadata)
		}
	}
	var version int64
	_ = store.Pool.QueryRow(ctx, `SELECT version FROM plan_steps WHERE id='alina-P5'`).Scan(&version)
	if version != 1 {
		t.Errorf("alina-P5 changed by maxim: version %d", version)
	}
	if w := request(h, "POST", "/api/steps/alina-P5/confirm", `{"confirmation_id":"`+sc.ID+`"}`, isoOrigin, alina); w.Code != 200 {
		t.Errorf("alina's own step confirmation broken: %d", w.Code)
	}
}

// П.5: смена профиля в одном браузере, logout, повторный вход, поздняя обработка.
func TestIsoSessionSwitchInOneBrowser(t *testing.T) {
	store, b, _, h := businessFixture(t)
	now := store.OperationalClock.Now()
	alina := isoLogin(t, h, "alina", nil)
	isoPostText(t, h, alina, actions.ID(), "Привет "+alinaMarker)
	card := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"space-payments","level":"read","justification":"Для задач стажировки в платежах"}}`)
	conf := isoConfirm(t, h, alina, card)
	// Тот же браузер: вход Максима с cookie Алины ротирует сессию.
	maxim := isoLogin(t, h, "maxim", alina)
	for _, p := range []struct{ method, path, body string }{{"GET", "/api/me", ""}, {"GET", "/api/chat", ""}, {"GET", "/api/onboarding", ""}, {"GET", "/api/attempts/" + conf.Attempt, ""}, {"POST", "/api/chat/messages", `{"client_message_id":"` + actions.ID() + `","text":"привет"}`}} {
		origin := ""
		if p.method != "GET" {
			origin = isoOrigin
		}
		if w := request(h, p.method, p.path, p.body, origin, alina); w.Code != 401 || strings.Contains(w.Body.String(), "Алина") {
			t.Errorf("rotated alina cookie still works on %s: %d", p.path, w.Code)
		}
	}
	// Подтверждённая Алиной операция обрабатывается после смены профиля от её имени.
	isoDrain(t, b, now)
	me := request(h, "GET", "/api/me", "", "", maxim).Body.String()
	_, mchat := isoGetChat(t, h, maxim, "")
	if !strings.Contains(me, `"id":"maxim"`) || strings.Contains(mchat, alinaMarker) || strings.Contains(mchat, "space-payments") {
		t.Error("maxim sees alina's state after switch")
	}
	if w := request(h, "GET", "/api/attempts/"+conf.Attempt, "", "", maxim); w.Code != 404 {
		t.Errorf("maxim reads alina's attempt after switch: %d", w.Code)
	}
	if w := request(h, "DELETE", "/api/session", "", isoOrigin, maxim); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/me", "", "", maxim); w.Code != 401 {
		t.Errorf("logout did not end session: %d", w.Code)
	}
	again := isoLogin(t, h, "alina", maxim)
	chat, body := isoGetChat(t, h, again, "")
	welcomes := strings.Count(body, "Привет, Алина Соколова!")
	if !strings.Contains(body, alinaMarker) || welcomes != 1 || strings.Contains(body, maximMarker) {
		t.Errorf("relogin history: marker=%v welcomes=%d", strings.Contains(body, alinaMarker), welcomes)
	}
	if w := request(h, "GET", "/api/attempts/"+conf.Attempt, "", "", again); w.Code != 200 || !strings.Contains(w.Body.String(), "succeeded") {
		t.Errorf("alina does not get her attempt after return: %d", w.Code)
	}
	t.Logf("relogin items=%d welcomes=%d", len(chat.Items), welcomes)
}

// П.6: проекции ролей и документы чужой команды.
func TestIsoRoleProjectionsAndTeamDocuments(t *testing.T) {
	world := isoScenario(t)
	h := world.h
	type planBody struct {
		Steps []struct {
			ID      string `json:"id"`
			OwnerID string `json:"owner_id"`
			Intern  string `json:"intern_id"`
		} `json:"steps"`
		Requests []json.RawMessage `json:"requests"`
	}
	expect := map[string]struct {
		owned  []string
		intern string
	}{
		"igor":   {[]string{"alina-P1", "alina-P2", "alina-P8"}, "alina"},
		"sergey": {[]string{"maxim-P1", "maxim-P2", "maxim-P8"}, "maxim"},
		"olga":   {[]string{"alina-P3"}, "alina"},
		"anna":   {[]string{"maxim-P3"}, "maxim"},
		"daria":  {[]string{"alina-HR"}, "alina"},
	}
	for user, want := range expect {
		ck := isoLogin(t, h, user, nil)
		raw := request(h, "GET", "/api/onboarding", "", "", ck)
		var plan planBody
		if raw.Code != 200 || json.Unmarshal(raw.Body.Bytes(), &plan) != nil {
			t.Fatalf("%s plan %d", user, raw.Code)
		}
		ids := map[string]bool{}
		for _, s := range plan.Steps {
			ids[s.ID] = true
			if s.OwnerID != user {
				t.Errorf("%s sees unassigned step %s (owner %s)", user, s.ID, s.OwnerID)
			}
		}
		for _, id := range want.owned {
			if !ids[id] {
				t.Errorf("%s misses assigned step %s", user, id)
			}
		}
		if len(plan.Steps) != len(want.owned) || len(plan.Requests) != 0 {
			t.Errorf("%s projection: steps=%d requests=%d", user, len(plan.Steps), len(plan.Requests))
		}
		_, chat := isoGetChat(t, h, ck, "")
		for _, m := range append([]string{alinaMarker, maximMarker, "ACC-", "ABS-"}, "Для задач стажировки") {
			if strings.Contains(chat, m) || strings.Contains(raw.Body.String(), m) {
				t.Errorf("%s sees intern data %q", user, m)
			}
		}
		// Прямой API к объектам стажёров.
		for _, intern := range []string{"alina", "maxim"} {
			for _, c := range world.confirms[intern] {
				if w := request(h, "GET", "/api/attempts/"+c.Attempt, "", "", ck); w.Code != 404 {
					t.Errorf("%s reads %s attempt: %d", user, intern, w.Code)
				}
			}
		}
	}
	// Стажёр видит шаги других владельцев в своём плане, но не их переписку.
	var plan planBody
	_ = json.Unmarshal(request(h, "GET", "/api/onboarding", "", "", world.alina).Body.Bytes(), &plan)
	others := 0
	for _, s := range plan.Steps {
		if s.Intern != "alina" {
			t.Errorf("alina plan has foreign intern step %s", s.ID)
		}
		if s.OwnerID != "alina" {
			others++
		}
	}
	if others == 0 {
		t.Error("alina does not see steps owned by buddy/manager/HR")
	}
	// Документы команды по прямой ссылке.
	docs := []struct {
		user, doc string
		want      int
	}{
		{"alina", "doc-pay-principles", 200}, {"alina", "doc-srch-components", 404}, {"alina", "doc-srch-notes", 404},
		{"maxim", "doc-pay-principles", 404}, {"maxim", "doc-pay-oncall-2024", 404}, {"maxim", "doc-srch-components", 200},
		{"igor", "doc-srch-components", 404}, {"sergey", "doc-pay-principles", 404},
		{"daria", "doc-pay-principles", 404}, {"daria", "doc-access-howto", 200},
	}
	missing := ""
	for _, d := range docs {
		ck := isoLogin(t, h, d.user, nil)
		w := request(h, "GET", "/demo/docs/"+d.doc, "", "", ck)
		if w.Code != d.want {
			t.Errorf("%s doc %s: %d want %d", d.user, d.doc, w.Code, d.want)
		}
		if w.Code == 404 {
			nf := normalizedError(request(h, "GET", "/demo/docs/doc-does-not-exist", "", "", ck).Body.String())
			if missing == "" {
				missing = nf
			}
			if normalizedError(w.Body.String()) != nf {
				t.Errorf("foreign document response differs from unknown document")
			}
		}
	}
}

// П.8: побочные каналы — события и метрики привязаны к владельцу.
func TestIsoEventsAndCountersAreOwnerScoped(t *testing.T) {
	world := isoScenario(t)
	ctx := context.Background()
	store := world.b.Store
	var mixed int
	if e := store.Pool.QueryRow(ctx, `SELECT count(*) FROM events e LEFT JOIN attempts a ON a.id=e.attempt_id WHERE a.id IS NOT NULL AND a.user_id<>e.user_id`).Scan(&mixed); e != nil {
		t.Fatal(e)
	}
	if mixed != 0 {
		t.Errorf("%d events attributed to another user", mixed)
	}
	var foreignLinks int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_links l JOIN execution_runs r ON r.id=l.run_id WHERE r.user_id<>l.user_id`).Scan(&foreignLinks)
	var foreignCardLinks int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_links l JOIN cards c ON l.kind='card' AND l.object_id=c.id::text WHERE c.user_id<>l.user_id`).Scan(&foreignCardLinks)
	var foreignNotify int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications n JOIN messages m ON m.id=n.message_id JOIN service_objects o ON o.service=n.service AND o.object_id=n.object_id WHERE m.user_id<>o.user_id`).Scan(&foreignNotify)
	if foreignLinks+foreignCardLinks+foreignNotify != 0 {
		t.Errorf("cross-user links=%d card_links=%d notifications=%d", foreignLinks, foreignCardLinks, foreignNotify)
	}
	var texts int
	_ = store.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE code||scenario||outcome ILIKE ANY(ARRAY['%`+alinaMarker+`%','%`+maximMarker+`%'])`).Scan(&texts)
	if texts != 0 {
		t.Errorf("event fields contain message text")
	}
	// Ответ API не несёт счётчиков, которые зависят от активности другого пользователя:
	// next_cursor — личная последовательность, а не глобальный id.
	chatA, _ := isoGetChat(t, world.h, world.alina, "")
	chatM, _ := isoGetChat(t, world.h, world.maxim, "")
	t.Logf("next_cursor alina=%d maxim=%d", chatA.Next, chatM.Next)
	for _, item := range chatM.Items {
		if item.Sequence > int64(len(chatM.Items)) {
			t.Errorf("maxim sequence %d reveals foreign activity", item.Sequence)
		}
	}
}

// П.8 (P3, демонстрация): message_id и id элементов чата — глобальная
// последовательность IDENTITY. Разница между двумя id Максима раскрывает,
// сколько сообщений за это время появилось у других пользователей.
// Тест ПАДАЕТ на текущем коде.
func TestIsoSideChannelGlobalMessageIDs(t *testing.T) {
	_, _, _, h := businessFixture(t)
	alina, maxim := isoLogin(t, h, "alina", nil), isoLogin(t, h, "maxim", nil)
	_, before, _ := isoPostText(t, h, maxim, actions.ID(), "Привет")
	for i := 0; i < 3; i++ {
		isoPostText(t, h, alina, actions.ID(), "Привет")
	}
	_, after, _ := isoPostText(t, h, maxim, actions.ID(), "Привет снова")
	b1, _ := strconv.ParseInt(before, 10, 64)
	a1, _ := strconv.ParseInt(after, 10, 64)
	chat, _ := isoGetChat(t, h, maxim, "")
	ids := []int64{}
	for _, item := range chat.Items {
		ids = append(ids, item.ID)
	}
	t.Logf("maxim message_id %d -> %d; chat ids=%v; gap=%d (alina sent 3)", b1, a1, ids, a1-b1-1)
	if a1-b1-1 != 0 {
		t.Fatalf("message_id reveals %d foreign messages between two own messages", a1-b1-1)
	}
}

func alinaSafeText() safety.Text {
	return safety.Prepare(context.Background(), safety.Local{}, "поздний ответ старого обработчика")
}

// IR-01 (P2, демонстрация): локальная проекция service_objects не привязана к
// владельцу. Если сервис (после выборочного пересоздания БД имитаций,
// восстановления из копии или ошибки адаптера) выдаст тот же номер заявки
// другому пользователю, upsert перезапишет payload, оставив прежний user_id.
// Тест ПАДАЕТ на текущем коде: заявка Максима видна в плане Алины.
func TestIsoServiceObjectOwnerNotBound(t *testing.T) {
	store, _, _, h := businessFixture(t)
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina := isoLogin(t, h, "alina", nil)
	created := now.Add(-time.Hour)
	// Доступ: случайно защищён — projectAccessPlan ищет строку по новому user_id,
	// не находит её и откатывает транзакцию (ошибка вместо перезаписи).
	own := actions.Object{Service: "access", ID: "ACC-1042", UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для задач стажировки в платежах", ApproverID: "olga", Status: "created", Version: 1, CreatedAt: created, UpdatedAt: created}
	if e := store.ApplyObject(ctx, "alina", own, now); e != nil {
		t.Fatal(e)
	}
	reused := actions.Object{Service: "access", ID: "ACC-1042", UserID: "maxim", ResourceID: "space-search", Level: "edit", Justification: "Для закрытой задачи " + maximMarker, ApproverID: "anna", Status: "in_review", Version: 2, CreatedAt: now, UpdatedAt: now}
	accessErr := store.ApplyObject(ctx, "maxim", reused, now)
	var accessOwner string
	_ = store.Pool.QueryRow(ctx, `SELECT payload->>'user_id' FROM service_objects WHERE service='access' AND object_id='ACC-1042'`).Scan(&accessOwner)
	t.Logf("access reuse: err=%v payload.user_id=%s", accessErr, accessOwner)
	// Отгул: проекции плана нет, upsert фиксируется.
	ownAbs := actions.Object{Service: "time_off", ID: "ABS-2001", UserID: "alina", StartDate: "2026-11-16", EndDate: "2026-11-16", Workdays: 1, ApproverID: "olga", Status: "submitted", Version: 1, CreatedAt: created, UpdatedAt: created}
	if e := store.ApplyObject(ctx, "alina", ownAbs, now); e != nil {
		t.Fatal(e)
	}
	reason := "Причина для " + maximMarker
	reusedAbs := actions.Object{Service: "time_off", ID: "ABS-2001", UserID: "maxim", StartDate: "2026-11-23", EndDate: "2026-11-27", Workdays: 5, ApproverID: "anna", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
	err := store.ApplyObject(ctx, "maxim", reusedAbs, now)
	var rowOwner, payloadOwner string
	_ = store.Pool.QueryRow(ctx, `SELECT user_id,payload->>'user_id' FROM service_objects WHERE service='time_off' AND object_id='ABS-2001'`).Scan(&rowOwner, &payloadOwner)
	plan := request(h, "GET", "/api/onboarding", "", "", alina).Body.String()
	leak := strings.Contains(plan, maximMarker) || strings.Contains(plan, "2026-11-23")
	t.Logf("time_off reuse: err=%v row.user_id=%s payload.user_id=%s alina_plan_has_maxim=%v", err, rowOwner, payloadOwner, leak)
	if rowOwner != payloadOwner || leak {
		t.Fatalf("service object reassigned in payload only: row=%s payload=%s; alina plan exposes maxim's dates/reason=%v", rowOwner, payloadOwner, leak)
	}
}

// IR-02 (P3, демонстрация): OfferNext связывает dialogue_state с карточкой по
// card_id без проверки владельца. Через API card_id задаёт только сервер, поэтому
// состояние подделывается напрямую в БД. Тест ПАДАЕТ на текущем коде: состояние
// карточки Алины порождает сообщение в ленте Максима.
func TestIsoOfferNextJoinsForeignCard(t *testing.T) {
	store, _, _, h := businessFixture(t)
	ctx := context.Background()
	now := store.OperationalClock.Now()
	alina, maxim := isoLogin(t, h, "alina", nil), isoLogin(t, h, "maxim", nil)
	isoGetChat(t, h, maxim, "")
	card := isoCreateCard(t, h, alina, `{"type":"access_create","parameters":{"resource_id":"monitoring","level":"read","justification":"Для задач стажировки по мониторингу"}}`)
	if w := request(h, "POST", "/api/cards/"+card.ID+"/discard", `{"expected_version":1}`, isoOrigin, alina); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `INSERT INTO dialogue_state(user_id,payload) VALUES('maxim',jsonb_build_object('card_id',$1::text,'pending',jsonb_build_array(jsonb_build_object('name','time_off_request')),'stage','waiting_result')) ON CONFLICT(user_id) DO UPDATE SET payload=EXCLUDED.payload`, card.ID); e != nil {
		t.Fatal(e)
	}
	before, _ := store.Messages(ctx, "maxim", 0)
	offered, e := store.OfferNext(ctx, now)
	after, _ := store.Messages(ctx, "maxim", 0)
	t.Logf("offered=%v err=%v maxim messages %d -> %d", offered, e, len(before), len(after))
	if len(after) != len(before) {
		t.Fatalf("alina's card state produced a message in maxim's chat: %q", after[len(after)-1].Text)
	}
}
