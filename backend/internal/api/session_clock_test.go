package api

import (
	"ai-buddy/backend/platform/clock"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type movableClock struct {
	mu      sync.Mutex
	instant time.Time
}

func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instant
}
func (c *movableClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.instant = c.instant.Add(d)
	c.mu.Unlock()
}

// Session lifetime follows operational time; business time (DEMO_NOW) keeps
// deciding start-date capabilities and never extends a session.
func TestSessionLifetimeUsesOperationalClock(t *testing.T) {
	demoNow := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		operational time.Duration
		business    time.Duration
		want        int
	}{
		{"fresh", 0, 0, 200},
		{"within ttl", 59 * time.Minute, 0, 200},
		{"expired with frozen demo date", time.Hour, 0, 401},
		{"long after with frozen demo date", 72 * time.Hour, 0, 401},
		{"business time moves, session does not", 0, 48 * time.Hour, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			operational := &movableClock{instant: demoNow.Add(-24 * time.Hour)}
			business := &movableClock{instant: demoNow}
			h := New(fakeStore(), Config{Demo: true, Origin: "http://localhost:5173", Clock: business, SessionClock: operational, SessionTTL: time.Hour, Location: time.FixedZone("demo", 5*3600)})
			login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
			if login.Code != 200 {
				t.Fatal(login)
			}
			cookie := login.Result().Cookies()[0]
			operational.Advance(c.operational)
			business.Advance(c.business)
			w := request(h, "GET", "/api/me", "", "", cookie)
			if w.Code != c.want {
				t.Fatalf("got %d want %d: %s", w.Code, c.want, w.Body.String())
			}
			if w.Code == 401 {
				var body map[string]any
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["profile"] != nil {
					t.Fatal("expired session leaked profile", w.Body.String())
				}
				return
			}
			var me struct {
				Capabilities map[string]bool `json:"capabilities"`
			}
			if json.Unmarshal(w.Body.Bytes(), &me) != nil || !me.Capabilities["service_actions"] {
				t.Fatal("business-clock capability changed", w.Body.String())
			}
		})
	}
}

func TestSessionClockDefaultsToSystem(t *testing.T) {
	frozen := clock.Fixed{Instant: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := fakeStore()
	h := New(store, Config{Demo: true, Origin: "http://localhost:5173", Clock: frozen, SessionTTL: time.Hour, Location: time.UTC})
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
	if login.Code != 200 {
		t.Fatal(login)
	}
	session := store.sessions[hash(login.Result().Cookies()[0].Value)]
	if !session.expires.After(time.Now().Add(59*time.Minute)) || session.expires.After(time.Now().Add(61*time.Minute)) {
		t.Fatalf("expiry %s is not measured from the system clock", session.expires)
	}
}
