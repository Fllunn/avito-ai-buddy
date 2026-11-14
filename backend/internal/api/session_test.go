package api

import (
	"ai-buddy/backend/internal/authz"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
)

type fakeIdentities struct {
	afterSteps func()
	lookupFail bool
	mu         sync.Mutex
	profiles   map[string]demo.Profile
	sessions   map[string]struct {
		user    string
		expires time.Time
	}
	fail bool
}

func fakeStore() *fakeIdentities {
	s := &fakeIdentities{profiles: map[string]demo.Profile{}, sessions: map[string]struct {
		user    string
		expires time.Time
	}{}}
	for _, p := range demo.Profiles() {
		s.profiles[p.ID] = p
	}
	return s
}
func (s *fakeIdentities) Profile(_ context.Context, id string) (demo.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupFail {
		return demo.Profile{}, errors.New("private database error")
	}
	p, ok := s.profiles[id]
	if !ok || !p.Enabled {
		return demo.Profile{}, authz.ErrNotFound
	}
	return p, nil
}
func (s *fakeIdentities) ReplaceSession(_ context.Context, old, token, user string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("secret raw failure")
	}
	delete(s.sessions, old)
	s.sessions[token] = struct {
		user    string
		expires time.Time
	}{user, expires}
	return nil
}
func (s *fakeIdentities) SessionProfile(ctx context.Context, token string, now time.Time) (demo.Profile, error) {
	s.mu.Lock()
	session, ok := s.sessions[token]
	s.mu.Unlock()
	if !ok || !now.Before(session.expires) {
		return demo.Profile{}, authz.ErrUnauthenticated
	}
	p, err := s.Profile(ctx, session.user)
	if errors.Is(err, authz.ErrNotFound) {
		return p, authz.ErrUnauthenticated
	}
	return p, err
}
func (s *fakeIdentities) DeleteSession(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("secret raw failure")
	}
	delete(s.sessions, token)
	return nil
}
func testServer(s IdentityStore, demoMode bool) http.Handler {
	now, _ := time.Parse(time.RFC3339, "2026-11-12T07:00:00Z")
	return New(s, Config{Demo: demoMode, Origin: "http://localhost:5173", Clock: clock.Fixed{Instant: now}, SessionTTL: time.Hour, Location: time.FixedZone("demo", 5*3600)})
}
func request(h http.Handler, method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestSessionLifecycleAndIdentity(t *testing.T) {
	store := fakeStore()
	h := testServer(store, true)
	origin := "http://localhost:5173"
	if w := request(h, "GET", "/api/me", "", "", nil); w.Code != 401 {
		t.Fatal(w)
	}
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatal(login)
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 3600 || len(cookie.Value) < 40 {
		t.Fatal(cookie)
	}
	if _, ok := store.sessions[cookie.Value]; ok {
		t.Fatal("plaintext cookie stored")
	}
	me := request(h, "GET", "/api/me?user_id=maxim&role=manager", "", "", cookie)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"id":"alina"`) || strings.Contains(me.Body.String(), "Максим") {
		t.Fatal(me)
	}
	other := request(h, "POST", "/api/demo/session", `{"user_id":"maxim"}`, origin, cookie)
	if other.Code != 200 {
		t.Fatal(other)
	}
	if w := request(h, "GET", "/api/me", "", "", cookie); w.Code != 401 {
		t.Fatal("previous identity survived rotation", w)
	}
	cookie = other.Result().Cookies()[0]
	if w := request(h, "DELETE", "/api/session", "", origin, cookie); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(w)
	}
	if w := request(h, "GET", "/api/me", "", "", cookie); w.Code != 401 {
		t.Fatal(w)
	}
	if w := request(h, "DELETE", "/api/session", "", origin, nil); w.Code != 204 {
		t.Fatal(w)
	}
}
func TestOriginAndMalformedInput(t *testing.T) {
	h := testServer(fakeStore(), true)
	for _, origin := range []string{"", "https://evil.example", "null"} {
		if w := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil); w.Code != 403 {
			t.Fatal(w)
		}
	}
	for _, body := range []string{`{"user_id":"alina","role":"manager"}`, `{"user_id":"alina","team_id":"search"}`, `{}`, `{"user_id":"missing"}`, `null`, `{"user_id":"alina"} {}`, strings.Repeat("x", 5000)} {
		w := request(h, "POST", "/api/demo/session", body, "http://localhost:5173", nil)
		if w.Code != 400 && w.Code != 401 {
			t.Fatal(body, w)
		}
	}
	r := httptest.NewRequest("POST", "/api/demo/session", strings.NewReader(`{"user_id":"alina"}`))
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w)
	}
	if w := request(h, "OPTIONS", "/api/me", "", "http://localhost:5173", nil); w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal(w)
	}
	if w := request(h, "OPTIONS", "/api/me", "", "", nil); w.Code != 403 {
		t.Fatal(w)
	}
}
func TestExpiredRevokedFutureAndRoles(t *testing.T) {
	store := fakeStore()
	h := testServer(store, true)
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
	cookie := login.Result().Cookies()[0]
	session := store.sessions[hash(cookie.Value)]
	session.expires = time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	store.sessions[hash(cookie.Value)] = session
	if w := request(h, "GET", "/api/me", "", "", cookie); w.Code != 401 {
		t.Fatal("expiry boundary", w)
	}
	for _, id := range []string{"alina", "igor", "olga", "daria"} {
		login = request(h, "POST", "/api/demo/session", `{"user_id":"`+id+`"}`, "http://localhost:5173", nil)
		cookie = login.Result().Cookies()[0]
		if id == "alina" {
			p := store.profiles[id]
			p.StartDate = "2026-11-13"
			store.profiles[id] = p
		}
		me := request(h, "GET", "/api/me", "", "", cookie)
		if me.Code != 200 || !strings.Contains(me.Body.String(), `"service_actions":false`) {
			t.Fatal(me)
		}
	}
	p := store.profiles["daria"]
	p.Enabled = false
	store.profiles["daria"] = p
	if w := request(h, "GET", "/api/me", "", "", cookie); w.Code != 401 {
		t.Fatal("disabled user retained session")
	}
}
func TestDisabledDemoAndStorageFailure(t *testing.T) {
	store := fakeStore()
	if w := request(testServer(store, false), "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil); w.Code != 404 {
		t.Fatal(w)
	}
	store.fail = true
	h := testServer(store, true)
	for _, method := range []string{"POST", "DELETE"} {
		path := "/api/demo/session"
		if method == "DELETE" {
			path = "/api/session"
		}
		w := request(h, method, path, `{"user_id":"alina"}`, "http://localhost:5173", nil)
		if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w)
		}
	}
}

func (s *fakeIdentities) Steps(_ context.Context, _ string) ([]demo.Step, error) {
	if s.afterSteps != nil {
		s.afterSteps()
	}
	if s.fail {
		return nil, errors.New("private database error")
	}
	return demo.Steps(), nil
}

func TestPlanRechecksIdentityBeforeDisclosure(t *testing.T) {
	for _, change := range []string{"version", "team", "disabled", "session", "outage"} {
		t.Run(change, func(t *testing.T) {
			store := fakeStore()
			h := testServer(store, true)
			login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
			cookie := login.Result().Cookies()[0]
			store.afterSteps = func() {
				p := store.profiles["alina"]
				switch change {
				case "version":
					p.PolicyVersion++
				case "team":
					team := "search"
					p.TeamID = &team
				case "disabled":
					p.Enabled = false
				case "session":
					delete(store.sessions, hash(cookie.Value))
				case "outage":
					store.lookupFail = true
				}
				store.profiles["alina"] = p
			}
			w := request(h, "GET", "/api/onboarding", "", "", cookie)
			want := 403
			if change == "disabled" || change == "session" {
				want = 401
			}
			if change == "outage" {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "alina-P") || strings.Contains(w.Body.String(), "steps") {
				t.Fatal("stale plan disclosed", w)
			}
		})
	}
}

func TestFuturePlanIsReadOnlyForAllRolesProjection(t *testing.T) {
	store := fakeStore()
	p := store.profiles["alina"]
	p.StartDate = "2026-11-13"
	store.profiles[p.ID] = p
	h := testServer(store, true)
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
	w := request(h, "GET", "/api/onboarding", "", "", login.Result().Cookies()[0])
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"read_only":true`) || !strings.Contains(w.Body.String(), "alina-P1") {
		t.Fatal("future plan unavailable or writable", w)
	}
}
func TestPlanProjectionCannotRequestOtherUser(t *testing.T) {
	store := fakeStore()
	h := testServer(store, true)
	if w := request(h, "GET", "/api/onboarding", "", "", nil); w.Code != 401 {
		t.Fatal(w)
	}
	for _, id := range []string{"alina", "igor", "daria"} {
		login := request(h, "POST", "/api/demo/session", `{"user_id":"`+id+`"}`, "http://localhost:5173", nil)
		w := request(h, "GET", "/api/onboarding?user_id=maxim", "", "", login.Result().Cookies()[0])
		if w.Code != 200 || strings.Contains(w.Body.String(), "maxim-") || strings.Contains(w.Body.String(), "sergey") {
			t.Fatal("foreign plan exposed", w)
		}
	}
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, "http://localhost:5173", nil)
	store.fail = true
	w := request(h, "GET", "/api/onboarding", "", "", login.Result().Cookies()[0])
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w)
	}
}

func TestIdentityOutageIsNotMistakenForLogout(t *testing.T) {
	store := fakeStore()
	h := testServer(store, true)
	origin := "http://localhost:5173"
	login := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil)
	cookie := login.Result().Cookies()[0]
	store.lookupFail = true
	for _, path := range []string{"/api/me", "/api/onboarding"} {
		w := request(h, "GET", path, "", "", cookie)
		if w.Code != 503 || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w)
		}
	}
	w := request(h, "POST", "/api/demo/session", `{"user_id":"alina"}`, origin, nil)
	if w.Code != 503 {
		t.Fatal(w)
	}
}
