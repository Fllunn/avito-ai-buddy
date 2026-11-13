package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	for _, service := range []string{"api", "mocks"} {
		h := Handler(service)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		var payload map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || payload["service"] != service || payload["status"] != "alive" || payload["stage"] != "bootstrap" {
			t.Fatal(w)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if w.Code != 503 || payload["code"] != "BOOTSTRAP_ONLY" {
			t.Fatal("bootstrap must not claim readiness")
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/healthz", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal("unexpected mutation allowed")
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/me", nil))
		if w.Code != http.StatusNotFound {
			t.Fatal("personal API not implemented")
		}
	}
}
