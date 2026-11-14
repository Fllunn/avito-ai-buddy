package agent

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/catalog"
	"ai-buddy/backend/internal/storage"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSensitiveCorpusAcrossStorageJobsAndProvider(t *testing.T) {
	databaseURL := os.Getenv("TEST_API_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("run make coverage for PostgreSQL checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := storage.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// Other Go packages test the same database concurrently. A dedicated schema
	// keeps this corpus and its migrations outside their seeded fixture counts.
	schema := pgx.Identifier{"safety_" + strings.ReplaceAll(actions.ID(), "-", "")}.Sanitize()
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Pool.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	store, err := storage.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user := "safety-" + actions.ID()
	if _, err := store.Pool.Exec(ctx, `INSERT INTO users(id,name,role,start_date) VALUES($1,'Синтетический тест','intern','2026-11-01')`, user); err != nil {
		t.Fatal(err)
	}
	p := &provider{raw: []byte(valid)}
	r := Recognizer{Builder: Builder{Source: store, Directory: catalog.Directory{Source: store}}, Provider: p, Timeout: time.Second}
	for _, tc := range []struct {
		raw, private string
		form         bool
	}{
		{"Хочу отгул: диагноз диабет", "диабет", true},
		{"Нужен день: мама заболела", "заболела", true},
		{"Паспорт 1234 567890", "1234 567890", true},
		{"У меня температура, надо к врачу", "температура", true},
		{"Отгул 13 ноября, запись в поликлинику", "поликлинику", true},
		{"Доступ к PAY, token=synthetic-opaque-value", "synthetic-opaque-value", false},
		{"Доступ, API_KEY=synthetic-api-value", "synthetic-api-value", false},
		{"Доступ, 4111 1111 1111 1111", "4111 1111 1111 1111", false},
	} {
		safe := safety.Prepare(ctx, safety.Local{}, tc.raw)
		m, _, err := store.AcceptMessage(ctx, user, actions.ID(), "text", "", safe, time.Now().UTC())
		if err != nil || m.FormRequired != tc.form {
			t.Fatal("corpus persistence failed", err)
		}
		var persisted, job string
		if err := store.Pool.QueryRow(ctx, `SELECT row_to_json(m)::text,row_to_json(j)::text FROM messages m JOIN jobs j ON j.message_id=m.id WHERE m.id=$1`, m.ID).Scan(&persisted, &job); err != nil {
			t.Fatal(err)
		}
		before := p.calls
		result := r.Recognize(ctx, user, safe)
		if tc.form && (p.calls != before || result.Code != "USE_FORM") {
			t.Fatal("closed input reached provider")
		}
		// A subsequent safe question rebuilds context from the persisted history.
		result = r.Recognize(ctx, user, text("С чего начать?"))
		if result.Fallback {
			t.Fatal(result)
		}
		frame, _ := json.Marshal(p.frame)
		output, _ := json.Marshal(result)
		for _, value := range []string{m.Text, persisted, job, string(frame), string(output), p.query} {
			if strings.Contains(value, tc.private) {
				t.Fatal("raw corpus detail crossed a protected boundary")
			}
		}
	}
}
