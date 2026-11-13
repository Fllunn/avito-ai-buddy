package config

import (
	"strings"
	"testing"
	"time"
)

func environment(overrides map[string]string) func(string) string {
	values := map[string]string{"DATABASE_URL": "postgres://api:synthetic@localhost:5432/buddy", "SERVICE_SECRET": strings.Repeat("x", 32)}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(environment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.RedisAddr != "localhost:6379" || c.MockURL != "http://localhost:8081" || c.AllowedOrigin != "http://localhost:5173" {
		t.Fatal("unexpected connection defaults")
	}
	if c.DemoMode || c.LLMProvider != "fake" || c.LLMModel != "" {
		t.Fatal("unexpected mode")
	}
	if c.CardTTL != 24*time.Hour || c.StepConfirmationTTL != 5*time.Minute || c.SessionTTL != 8*time.Hour || c.LLMTimeout != 20*time.Second || c.ServiceTimeout != 3*time.Second || c.ClientPollInterval != time.Second {
		t.Fatal("unexpected durations")
	}
	want := []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}
	for i, delay := range want {
		if c.ReconcileDelays[i] != delay {
			t.Fatal("unexpected schedule")
		}
	}
	if c.Location.String() != "Asia/Yekaterinburg" || c.Clock.Now().IsZero() {
		t.Fatal("missing clock")
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(environment(map[string]string{
		"HTTP_ADDR": "127.0.0.1:9000", "DATABASE_URL": "postgresql://localhost/buddy", "REDIS_ADDR": "[::1]:6379",
		"MOCK_URL": "https://mocks.example", "ALLOWED_ORIGIN": "https://app.example",
		"DEMO_MODE": "true", "DEMO_NOW": "2026-11-12T12:00:00+05:00", "DEMO_TIMEZONE": "UTC",
		"CARD_TTL": "48h", "STEP_CONFIRMATION_TTL": "10m", "SESSION_TTL": "1h", "LLM_TIMEOUT": "30s", "SERVICE_TIMEOUT": "5s", "CLIENT_POLL_INTERVAL": "2s",
		"RECONCILE_DELAYS": "2s, 4s,8s,16s,32s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.DemoMode || c.Clock.Now().Format(time.RFC3339) != "2026-11-12T07:00:00Z" || c.Location.String() != "UTC" {
		t.Fatal("incorrect demo clock")
	}
	if c.CardTTL != 48*time.Hour || c.ReconcileDelays[4] != 32*time.Second || c.HTTPAddr != "127.0.0.1:9000" {
		t.Fatal("overrides ignored")
	}
}

func TestInvalidSettings(t *testing.T) {
	cases := []struct{ key, value string }{
		{"DEMO_MODE", "perhaps"}, {"DEMO_NOW", "2026-11-12T12:00:00Z"}, {"DEMO_TIMEZONE", "Invalid/Zone"},
		{"HTTP_ADDR", "localhost"}, {"HTTP_ADDR", ":0"}, {"HTTP_ADDR", ":65536"}, {"HTTP_ADDR", ":http"},
		{"REDIS_ADDR", ":6379"}, {"REDIS_ADDR", "redis://localhost:6379"},
		{"DATABASE_URL", ""}, {"DATABASE_URL", "%"}, {"DATABASE_URL", "mysql://localhost/buddy"}, {"DATABASE_URL", "postgres:///buddy"}, {"DATABASE_URL", "postgres://localhost"},
		{"MOCK_URL", "%"}, {"MOCK_URL", "ftp://localhost"}, {"MOCK_URL", "http://localhost/path"}, {"MOCK_URL", "http://user:password@localhost"},
		{"ALLOWED_ORIGIN", "*"}, {"ALLOWED_ORIGIN", "http://localhost?"}, {"ALLOWED_ORIGIN", "http://localhost?x=y"}, {"ALLOWED_ORIGIN", "http://localhost#fragment"},
		{"SERVICE_SECRET", "short"}, {"LLM_PROVIDER", "unknown"}, {"LLM_MODEL", "real-model"},
		{"RECONCILE_DELAYS", "1s,2s"}, {"RECONCILE_DELAYS", "1s,2s,3s,4s,0s"}, {"RECONCILE_DELAYS", "1s,2s,3s,4s,4s"}, {"RECONCILE_DELAYS", "1s,2s,3s,4s,nope"},
	}
	for _, key := range []string{"CARD_TTL", "STEP_CONFIRMATION_TTL", "SESSION_TTL", "LLM_TIMEOUT", "SERVICE_TIMEOUT", "CLIENT_POLL_INTERVAL"} {
		for _, value := range []string{"0s", "-1s", "tomorrow", "999999999999999999h"} {
			cases = append(cases, struct{ key, value string }{key, value})
		}
	}
	for _, tc := range cases {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			_, err := Load(environment(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("want safe error naming %s, got %v", tc.key, err)
			}
		})
	}
	if _, err := Load(environment(map[string]string{"DEMO_MODE": "true", "DEMO_NOW": "invalid"})); err == nil {
		t.Fatal("accepted invalid demo instant")
	}
}

func TestErrorsDoNotExposeValues(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "MOCK_URL", "SERVICE_SECRET", "LLM_PROVIDER", "LLM_MODEL"} {
		secret := "sensitive-test-value"
		_, err := Load(environment(map[string]string{key: secret}))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe error for %s: %v", key, err)
		}
	}
}

func TestPresenterConfiguration(t *testing.T) {
	values := map[string]string{"DATABASE_URL": "postgres://demo:synthetic@localhost/demo", "SERVICE_SECRET": strings.Repeat("s", 32), "DEMO_ADMIN_SECRET": strings.Repeat("p", 32)}
	get := func(k string) string { return values[k] }
	if cfg, err := Load(get); err != nil || cfg.DemoPanelOrigin != "http://localhost:8081" {
		t.Fatal(cfg, err)
	}
	for _, value := range []string{"short", strings.Repeat("s", 32)} {
		values["DEMO_ADMIN_SECRET"] = value
		if _, err := Load(get); err == nil {
			t.Fatal("unsafe presenter secret accepted")
		}
	}
	values["DEMO_ADMIN_SECRET"] = strings.Repeat("p", 32)
	values["DEMO_PANEL_ORIGIN"] = "https://example.test/path"
	if _, err := Load(get); err == nil {
		t.Fatal("invalid presenter origin")
	}
}
