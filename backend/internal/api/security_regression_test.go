package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/agent"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

// Regression for security review SR-01: with a frozen business clock the
// stored session expiry follows operational time, not DEMO_NOW.
func TestSecuritySessionExpiryIgnoresDemoClock(t *testing.T) {
	store, b, _, h := businessFixture(t)
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	var expires time.Time
	if e := store.Pool.QueryRow(context.Background(), `SELECT max(expires_at) FROM sessions WHERE user_id='alina'`).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	if !expires.After(time.Now()) || !expires.After(b.Tools.Clock.Now().Add(time.Hour)) {
		t.Fatalf("expiry %s follows frozen business clock %s", expires, b.Tools.Clock.Now())
	}
}

type scriptedProvider struct {
	agent.Fake
	reply string
	err   error
}

func (p scriptedProvider) Recognize(context.Context, agent.Context, string) ([]byte, error) {
	return []byte(p.reply), p.err
}
func modelReply(t *testing.T, intent agent.Intent) string {
	t.Helper()
	intent.Confidence = 1
	raw, e := json.Marshal(agent.Intents{Intents: []agent.Intent{intent}, Ambiguities: []string{}})
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func finishFor(t *testing.T, b *Business, user, id string) {
	t.Helper()
	for range 15 {
		j, e := b.Store.Job(context.Background(), user, id)
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
func postButton(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie, user, intent string, slots map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"client_message_id": actions.ID(), "intent": intent, "slots": slots})
	w := request(h, "POST", "/api/dialogue/actions", string(raw), "http://localhost:5173", cookie)
	var body struct {
		Job string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Body.String())
	}
	finishFor(t, b, user, body.Job)
}

// Regression for security review SR-02: only the user's explicit action may
// continue a submission after a rejection; model output may not.
func TestSecurityReapplyConsentComesFromUser(t *testing.T) {
	reason := "Демонстрационный отказ"
	rejectedTimeOff := func(now time.Time) actions.Object {
		return actions.Object{Service: "time_off", ID: "ABS-9001", UserID: "alina", StartDate: "2026-11-16", EndDate: "2026-11-16", Workdays: 1, ApproverID: "olga", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
	}
	rejectedAccess := func(now time.Time) actions.Object {
		return actions.Object{Service: "access", ID: "ACC-9001", UserID: "alina", ResourceID: "monitoring", Level: "read", Justification: "Для прежней задачи мониторинга", ApproverID: "timur", Status: "rejected", Reason: &reason, Version: 2, CreatedAt: now, UpdatedAt: now}
	}
	reapplySlots := map[string]string{"start_date": "2026-11-16", "end_date": "2026-11-16", "query": reapplyQuery}
	cases := []struct {
		name  string
		user  string
		steps func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie)
		cards int
	}{
		{"time off: model reapply marker", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			b.Provider = scriptedProvider{reply: modelReply(t, agent.Intent{Name: "time_off_request", Slots: agent.Slots{StartDate: "2026-11-16", EndDate: "2026-11-16", Query: reapplyQuery}})}
			postText(t, b, h, cookie, "Нужен отгул на понедельник")
		}, 0},
		{"time off: user button", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			postButton(t, b, h, cookie, "alina", "time_off_request", reapplySlots)
		}, 1},
		{"time off: explicit yes after notice", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			b.Provider = scriptedProvider{reply: modelReply(t, agent.Intent{Name: "time_off_request", Slots: agent.Slots{StartDate: "2026-11-16", EndDate: "2026-11-16"}})}
			postText(t, b, h, cookie, "Нужен отгул на понедельник")
			postText(t, b, h, cookie, "да")
		}, 1},
		{"time off: model fails at reapply step", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			b.Provider = scriptedProvider{reply: modelReply(t, agent.Intent{Name: "time_off_request", Slots: agent.Slots{StartDate: "2026-11-16", EndDate: "2026-11-16"}})}
			postText(t, b, h, cookie, "Нужен отгул на понедельник")
			b.Provider = scriptedProvider{err: errors.New("synthetic provider outage")}
			postText(t, b, h, cookie, "ну оформи как-нибудь")
		}, 0},
		{"time off: other role button", "igor", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			postButton(t, b, h, cookie, "igor", "time_off_request", reapplySlots)
		}, 0},
		{"access: model justification after notice", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			postText(t, b, h, cookie, "Нужен доступ в monitoring")
			b.Provider = scriptedProvider{reply: modelReply(t, agent.Intent{Name: "access_request", Slots: agent.Slots{ResourceID: "monitoring", Level: "read", Justification: "Для задач мониторинга сервиса"}})}
			postText(t, b, h, cookie, "оформи снова")
		}, 0},
		{"access: model reapply marker", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			b.Provider = scriptedProvider{reply: modelReply(t, agent.Intent{Name: "access_request", Slots: agent.Slots{ResourceID: "monitoring", Level: "read", Justification: "Для задач мониторинга сервиса", Query: reapplyQuery}})}
			postText(t, b, h, cookie, "оформи доступ к мониторингу снова")
		}, 0},
		{"access: user own justification", "alina", func(t *testing.T, b *Business, h http.Handler, cookie *http.Cookie) {
			postText(t, b, h, cookie, "Нужен доступ в monitoring")
			postText(t, b, h, cookie, "Для задач мониторинга сервиса платежей")
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, b, remote, h := businessFixture(t)
			now := b.Tools.Clock.Now()
			remote.objects["ABS-9001"] = rejectedTimeOff(now)
			remote.objects["ACC-9001"] = rejectedAccess(now)
			cookie := request(h, "POST", "/api/demo/session", `{"user_id":"`+c.user+`"}`, "http://localhost:5173", nil).Result().Cookies()[0]
			c.steps(t, b, h, cookie)
			cards, e := store.Cards(context.Background(), c.user, now)
			if e != nil {
				t.Fatal(e)
			}
			if len(cards) != c.cards {
				t.Fatalf("cards=%d want %d", len(cards), c.cards)
			}
			if remote.count != 0 {
				t.Fatal("service called before card confirmation")
			}
		})
	}
}
