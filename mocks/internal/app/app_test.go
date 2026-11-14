package app

import (
	"ai-buddy/backend/platform/config"
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFactory(t *testing.T) {
	url := os.Getenv("TEST_MOCKS_DATABASE_URL")
	if url == "" {
		t.Skip("make coverage")
	}
	cfg, err := config.Load(func(key string) string {
		switch key {
		case "DATABASE_URL":
			return url
		case "DEMO_ADMIN_SECRET":
			return strings.Repeat("p", 32)
		case "SERVICE_SECRET":
			return strings.Repeat("x", 32)
		case "DEMO_MODE":
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	h, closeApp, err := Factory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeApp()
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{{"/healthz", 200, `"service":"mocks"`}, {"/readyz", 200, `"status":"ready"`}, {"/v1/access-requests", 401, "UNAUTHENTICATED"}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Fatal(w)
		}
	}
	r := httptest.NewRequest("GET", "/v1/resources", nil)
	r.Header.Set("Authorization", "Bearer "+cfg.ServiceSecret)
	r.Header.Set("X-Actor-ID", "alina")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "salary-reports") {
		t.Fatal(w)
	}
	cfg.DemoMode = false
	_, closeOther, err := Factory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOther()
}
func TestFactoryRejectsBadDatabase(t *testing.T) {
	if _, _, err := Factory(context.Background(), config.Config{DatabaseURL: "postgres://%zz"}); err == nil {
		t.Fatal("bad database accepted")
	}
}
