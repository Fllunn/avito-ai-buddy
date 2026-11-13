package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-buddy/backend/platform/clock"
)

type Config struct {
	HTTPAddr            string
	DatabaseURL         string
	RedisAddr           string
	AllowedOrigin       string
	MockURL             string
	ServiceSecret       string
	DemoAdminSecret     string
	DemoPanelOrigin     string
	DemoMode            bool
	Clock               clock.Clock
	Location            *time.Location
	CardTTL             time.Duration
	StepConfirmationTTL time.Duration
	SessionTTL          time.Duration
	LLMTimeout          time.Duration
	ServiceTimeout      time.Duration
	ClientPollInterval  time.Duration
	ReconcileDelays     []time.Duration
	LLMProvider         string
	LLMModel            string
}

// Load never includes supplied values in errors: URLs may contain credentials.
func Load(getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		HTTPAddr:        value("HTTP_ADDR", ":8080"),
		DatabaseURL:     getenv("DATABASE_URL"),
		RedisAddr:       value("REDIS_ADDR", "localhost:6379"),
		AllowedOrigin:   value("ALLOWED_ORIGIN", "http://localhost:5173"),
		MockURL:         value("MOCK_URL", "http://localhost:8081"),
		ServiceSecret:   getenv("SERVICE_SECRET"),
		DemoAdminSecret: getenv("DEMO_ADMIN_SECRET"),
		DemoPanelOrigin: value("DEMO_PANEL_ORIGIN", "http://localhost:8081"),
		LLMProvider:     value("LLM_PROVIDER", "fake"),
		LLMModel:        getenv("LLM_MODEL"),
	}
	var err error
	c.DemoMode, err = strconv.ParseBool(value("DEMO_MODE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid DEMO_MODE: expected boolean")
	}
	if !c.DemoMode && getenv("DEMO_NOW") != "" {
		return Config{}, fmt.Errorf("DEMO_NOW requires DEMO_MODE=true")
	}
	c.Clock, c.Location, err = clock.New(getenv("DEMO_NOW"), value("DEMO_TIMEZONE", "Asia/Yekaterinburg"))
	if err != nil {
		return Config{}, err
	}
	for key, address := range map[string]string{"HTTP_ADDR": c.HTTPAddr, "REDIS_ADDR": c.RedisAddr} {
		host, port, splitErr := net.SplitHostPort(address)
		n, portErr := strconv.Atoi(port)
		if splitErr != nil || portErr != nil || n < 1 || n > 65535 || (key == "REDIS_ADDR" && host == "") {
			return Config{}, fmt.Errorf("invalid %s: expected host:port", key)
		}
	}
	db, err := url.Parse(c.DatabaseURL)
	if err != nil || db == nil || (db.Scheme != "postgres" && db.Scheme != "postgresql") || db.Hostname() == "" || strings.Trim(db.Path, "/") == "" {
		return Config{}, fmt.Errorf("invalid DATABASE_URL: expected PostgreSQL database URL")
	}
	for key, raw := range map[string]string{"ALLOWED_ORIGIN": c.AllowedOrigin, "MOCK_URL": c.MockURL, "DEMO_PANEL_ORIGIN": c.DemoPanelOrigin} {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
			return Config{}, fmt.Errorf("invalid %s: expected HTTP origin", key)
		}
	}
	if len(c.ServiceSecret) < 32 {
		return Config{}, fmt.Errorf("SERVICE_SECRET must contain at least 32 bytes")
	}
	if c.DemoAdminSecret != "" && (len(c.DemoAdminSecret) < 32 || c.DemoAdminSecret == c.ServiceSecret) {
		return Config{}, fmt.Errorf("DEMO_ADMIN_SECRET must be distinct and contain at least 32 bytes")
	}
	if c.LLMProvider != "fake" {
		return Config{}, fmt.Errorf("LLM_PROVIDER adapter is not implemented; use fake")
	}
	if c.LLMModel != "" {
		return Config{}, fmt.Errorf("LLM_MODEL must be empty for fake provider")
	}
	settings := []struct {
		key, fallback string
		target        *time.Duration
	}{
		{"CARD_TTL", "24h", &c.CardTTL},
		{"STEP_CONFIRMATION_TTL", "5m", &c.StepConfirmationTTL},
		{"SESSION_TTL", "8h", &c.SessionTTL},
		{"LLM_TIMEOUT", "20s", &c.LLMTimeout},
		{"SERVICE_TIMEOUT", "3s", &c.ServiceTimeout},
		{"CLIENT_POLL_INTERVAL", "1s", &c.ClientPollInterval},
	}
	for _, setting := range settings {
		d, parseErr := time.ParseDuration(value(setting.key, setting.fallback))
		if parseErr != nil || d <= 0 {
			return Config{}, fmt.Errorf("invalid %s: expected positive duration", setting.key)
		}
		*setting.target = d
	}
	for _, raw := range strings.Split(value("RECONCILE_DELAYS", "1s,3s,10s,30s,60s"), ",") {
		d, parseErr := time.ParseDuration(strings.TrimSpace(raw))
		if parseErr != nil || d <= 0 || (len(c.ReconcileDelays) > 0 && d <= c.ReconcileDelays[len(c.ReconcileDelays)-1]) {
			return Config{}, fmt.Errorf("invalid RECONCILE_DELAYS: expected increasing positive durations")
		}
		c.ReconcileDelays = append(c.ReconcileDelays, d)
	}
	if len(c.ReconcileDelays) != 5 {
		return Config{}, fmt.Errorf("RECONCILE_DELAYS must contain five durations")
	}
	return c, nil
}
