package service

import (
	"ai-buddy/backend/platform/clock"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func panelFixture(t *testing.T) *Server {
	s := fixture(t)
	s.Config.PanelSecret = strings.Repeat("p", 32)
	s.Config.PanelOrigin = "http://localhost:8081"
	s.Config.OperationalClock = clock.Fixed{Instant: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	return s
}
func panelCall(s *Server, method, path, body, contentType, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func loginPresenter(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"secret": s.Config.PanelSecret})
	w := panelCall(s, "POST", "/demo/session", string(body), "application/json", s.Config.PanelOrigin, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w)
	}
	return w.Result().Cookies()[0]
}
func TestPresenterIdentityOriginExpiryAndLogout(t *testing.T) {
	s := panelFixture(t)
	if w := panelCall(s, "GET", "/demo", "", "", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Секрет ведущего") || strings.Contains(w.Body.String(), s.Config.PanelSecret) {
		t.Fatal(w)
	}
	user := &http.Cookie{Name: "buddy_session", Value: "user-session"}
	for _, cookie := range []*http.Cookie{nil, user} {
		w := panelCall(s, "PUT", "/demo/faults/access", `{"mode":"normal","delay_ms":0}`, "application/json", s.Config.PanelOrigin, cookie)
		if w.Code != 401 {
			t.Fatal("user controls panel", w)
		}
	}
	for _, secret := range []string{"wrong", s.Config.Secret} {
		w := panelCall(s, "POST", "/demo/session", `{"secret":"`+secret+`"}`, "application/json", s.Config.PanelOrigin, nil)
		if w.Code != 401 {
			t.Fatal("service secret authorized presenter", w)
		}
	}
	cookie := loginPresenter(t, s)
	if !cookie.HttpOnly || cookie.Path != "/demo" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
	var stored string
	if err := s.Store.Pool.QueryRow(context.Background(), `SELECT token_hash FROM presenter_sessions WHERE token_hash=$1`, tokenHash(cookie.Value)).Scan(&stored); err != nil || stored == cookie.Value {
		t.Fatal("raw token stored", err)
	}
	if w := panelCall(s, "PUT", "/demo/faults/access", `{"mode":"normal","delay_ms":0}`, "application/json", "http://evil.test", cookie); w.Code != 403 {
		t.Fatal(w)
	}
	w := panelCall(s, "GET", "/demo", "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Сбои сервиса") {
		t.Fatal(w)
	}
	next := panelCall(s, "POST", "/demo/session", `{"secret":"`+s.Config.PanelSecret+`"}`, "application/json", s.Config.PanelOrigin, cookie)
	if next.Code != 200 {
		t.Fatal(next)
	}
	if w := panelCall(s, "GET", "/demo", "", "", "", cookie); w.Code != 401 {
		t.Fatal("old presenter survived rotation", w)
	}
	cookie = next.Result().Cookies()[0]
	if w := panelCall(s, "DELETE", "/demo/session", "", "", s.Config.PanelOrigin, cookie); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(w)
	}
	if w := panelCall(s, "GET", "/demo", "", "", "", cookie); w.Code != 401 {
		t.Fatal("logout failed", w)
	}
	cookie = loginPresenter(t, s)
	s.Config.OperationalClock = clock.Fixed{Instant: s.panelNow().Add(time.Hour)}
	if w := panelCall(s, "GET", "/demo", "", "", "", cookie); w.Code != 401 {
		t.Fatal("expiry ignored frozen business clock", w)
	}
}
func TestPresenterControlsVersionsAndFaultModes(t *testing.T) {
	s := panelFixture(t)
	cookie := loginPresenter(t, s)
	object := create(t, s, "alina", "monitoring")
	for _, mode := range []string{"normal", "unavailable_before", "delay_before", "lose_response_after", "reconciliation_unavailable"} {
		w := panelCall(s, "PUT", "/demo/faults/access", `{"mode":"`+mode+`","delay_ms":5}`, "application/json", s.Config.PanelOrigin, cookie)
		if w.Code != 200 {
			t.Fatal(mode, w)
		}
	}
	for _, body := range []string{`{"mode":"invalid","delay_ms":0}`, `{"mode":"normal","delay_ms":-1}`, `{"mode":"normal","delay_ms":10001}`, `{"mode":"normal","delay_ms":0,"user_id":"maxim"}`} {
		if w := panelCall(s, "PUT", "/demo/faults/access", body, "application/json", s.Config.PanelOrigin, cookie); w.Code != 400 {
			t.Fatal(w)
		}
	}
	if w := panelCall(s, "PUT", "/demo/faults/unknown", `{"mode":"normal","delay_ms":0}`, "application/json", s.Config.PanelOrigin, cookie); w.Code != 404 {
		t.Fatal(w)
	}
	w := panelCall(s, "PATCH", "/demo/access-requests/"+object.ID+"/status", `{"expected_version":1,"status":"rejected","reason":"диагноз диабет"}`, "application/json", s.Config.PanelOrigin, cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), "диабет") {
		t.Fatal(w)
	}
	if w := panelCall(s, "PATCH", "/demo/access-requests/"+object.ID+"/status", `{"expected_version":1,"status":"approved"}`, "application/json", s.Config.PanelOrigin, cookie); w.Code != 409 {
		t.Fatal(w)
	}
	if w := panelCall(s, "PATCH", "/demo/access-requests/"+object.ID+"/status", `{"expected_version":2,"status":"approved","actor_id":"maxim"}`, "application/json", s.Config.PanelOrigin, cookie); w.Code != 400 {
		t.Fatal(w)
	}
}
func TestPresenterFormsAndEscaping(t *testing.T) {
	s := panelFixture(t)
	form := url.Values{"secret": {s.Config.PanelSecret}}
	login := panelCall(s, "POST", "/demo/session", form.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, nil)
	if login.Code != 303 {
		t.Fatal(login)
	}
	cookie := login.Result().Cookies()[0]
	object := create(t, s, "alina", "monitoring")
	status := url.Values{"id": {object.ID}, "version": {"1"}, "status": {"rejected"}, "reason": {"<script>alert(1)</script>"}}
	w := panelCall(s, "POST", "/demo/panel/status", status.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie)
	if w.Code != 303 {
		t.Fatal(w)
	}
	page := panelCall(s, "GET", "/demo", "", "", "", cookie)
	if page.Code != 200 || strings.Contains(page.Body.String(), "<script>") || !strings.Contains(page.Body.String(), "&lt;script&gt;") {
		t.Fatal(page)
	}
	fault := url.Values{"service": {"access"}, "mode": {"normal"}, "delay_ms": {"0"}}
	if w := panelCall(s, "POST", "/demo/panel/fault", fault.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 303 {
		t.Fatal(w)
	}
	fault.Set("delay_ms", "bad")
	if w := panelCall(s, "POST", "/demo/panel/fault", fault.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 400 {
		t.Fatal(w)
	}
	status.Set("version", "bad")
	if w := panelCall(s, "POST", "/demo/panel/status", status.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 400 {
		t.Fatal(w)
	}
	s.Config.Demo = false
	if w := panelCall(s, "GET", "/demo", "", "", "", cookie); w.Code != 404 {
		t.Fatal("presenter enabled outside demo")
	}
}

func TestMalformedPanelInputAndUnavailableStorage(t *testing.T) {
	s := panelFixture(t)
	cookie := loginPresenter(t, s)
	for _, body := range []string{`{}`, `not json`, `{"secret":"x","user_id":"alina"}`} {
		if w := panelCall(s, "POST", "/demo/session", body, "application/json", s.Config.PanelOrigin, nil); w.Code != 400 && w.Code != 401 {
			t.Fatal(w)
		}
	}
	if w := panelCall(s, "POST", "/demo/session", "secret=x&extra=y", "application/x-www-form-urlencoded", s.Config.PanelOrigin, nil); w.Code != 400 {
		t.Fatal(w)
	}
	for _, path := range []string{"/demo/panel/status", "/demo/panel/fault"} {
		if w := panelCall(s, "POST", path, "%bad", "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 400 {
			t.Fatal(w)
		}
	}
	form := url.Values{"service": {"unknown"}, "mode": {"normal"}, "delay_ms": {"0"}}
	if w := panelCall(s, "POST", "/demo/panel/fault", form.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 400 {
		t.Fatal(w)
	}
	form = url.Values{"id": {"ACC-999999"}, "version": {"1"}, "status": {"approved"}, "reason": {""}}
	if w := panelCall(s, "POST", "/demo/panel/status", form.Encode(), "application/x-www-form-urlencoded", s.Config.PanelOrigin, cookie); w.Code != 404 {
		t.Fatal(w)
	}
	s.Config.OperationalClock = nil
	before := time.Now().Add(-time.Second)
	if s.panelNow().Before(before) {
		t.Fatal("system operational clock")
	}
	// A separate closed pool leaves the fixture available for cleanup.
	closed, err := Open(context.Background(), s.Store.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	copyServer := *s
	copyServer.Store = closed
	if w := panelCall(&copyServer, "GET", "/demo", "", "", "", cookie); w.Code != 503 {
		t.Fatal(w)
	}
	if w := panelCall(&copyServer, "POST", "/demo/session", `{"secret":"`+s.Config.PanelSecret+`"}`, "application/json", s.Config.PanelOrigin, nil); w.Code != 503 {
		t.Fatal(w)
	}
}
