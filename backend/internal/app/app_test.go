package app

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/platform/config"
	"context"
	"github.com/jackc/pgx/v5"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFactory(t *testing.T) {
	databaseURL := os.Getenv("TEST_API_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("make integration-test")
	}
	admin, e := storage.Open(context.Background(), databaseURL)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := pgx.Identifier{"app_" + strings.ReplaceAll(actions.ID(), "-", "")}.Sanitize()
	if _, e = admin.Pool.Exec(context.Background(), "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	parsed, e := url.Parse(databaseURL)
	if e != nil {
		t.Fatal(e)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	databaseURL = parsed.String()
	cfg, err := config.Load(func(key string) string {
		switch key {
		case "DATABASE_URL":
			return databaseURL
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handler, closeApp, err := Factory(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeApp()
	r := httptest.NewRequest("POST", "/api/demo/session", strings.NewReader(`{"user_id":"alina"}`))
	r.Header.Set("Origin", cfg.AllowedOrigin)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w)
	}
	r = httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Алина") {
		t.Fatal(w)
	}
	cfg.DemoMode = false
	_, closeOther, err := Factory(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeOther()
	cfg.DatabaseURL = "postgres://%zz"
	if _, _, err := Factory(ctx, cfg); err == nil {
		t.Fatal("bad database accepted")
	}
}
