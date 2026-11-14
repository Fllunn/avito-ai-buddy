package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/execution"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExecutionPersistence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := ExecutionRepository{s}
	now := s.OperationalClock.Now()
	id := actions.ID()
	run := execution.Run{ID: id, UserID: "alina", Scenario: "access", State: "queued", StartedAt: now, DetailComplete: true}
	if e := r.Create(ctx, run); e != nil {
		t.Fatal(e)
	}
	fact := execution.Fact{Key: "context:1", Node: execution.Node{ID: "context", Kind: "context", LabelCode: "context", State: "succeeded", StartedAt: &now, FinishedAt: &now}, State: "running", At: now}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := r.Append(ctx, id, fact); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	fact.Key = "confirmation:1"
	fact.Node = execution.Node{ID: "confirmation", Kind: "confirmation", LabelCode: "confirmation", State: "waiting_user"}
	fact.State = "waiting_user"
	if e := r.Append(ctx, id, fact); e != nil {
		t.Fatal(e)
	}
	p, e := r.Read(ctx, "alina", id, 0, 1)
	if e != nil || p.NextSequence != 1 || p.Snapshot.LastSequence != 2 || !p.HasMore || len(p.Snapshot.Nodes) != 2 {
		t.Fatal(p, e)
	}
	p, e = r.Read(ctx, "alina", id, p.NextSequence, 1)
	if e != nil || p.NextSequence != 2 || p.HasMore || p.Snapshot.State != "waiting_user" {
		t.Fatal(p, e)
	}
	reopened, e := Open(ctx, s.Pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if p, e = (ExecutionRepository{reopened}).Read(ctx, "alina", id, 0, 100); e != nil || len(p.Events) != 2 {
		t.Fatal(p, e)
	}
	if _, e = r.Read(ctx, "maxim", id, 0, 1); e != execution.ErrMissing {
		t.Fatal(e)
	}
	for _, args := range []struct {
		after int64
		limit int
	}{{-1, 1}, {0, 0}, {0, 101}} {
		if _, e = r.Read(ctx, "alina", id, args.after, args.limit); e != execution.ErrInvalid {
			t.Fatal(e)
		}
	}
	if _, e = r.Read(ctx, "alina", "bad", 0, 1); e != execution.ErrMissing {
		t.Fatal(e)
	}
	if _, e = r.Read(ctx, "alina", actions.ID(), 0, 1); e != execution.ErrMissing {
		t.Fatal(e)
	}
	fact.Node.LabelCode = "salary-secret"
	if r.Append(ctx, id, fact) != execution.ErrInvalid {
		t.Fatal("unsafe label")
	}
	fact.Node.LabelCode = "confirmation"
	fact.Key = "late"
	fact.At = now.Add(-time.Second)
	if r.Append(ctx, id, fact) != execution.ErrConflict {
		t.Fatal("late fact")
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE users SET team_id=NULL WHERE id='alina'`); e != nil {
		t.Fatal(e)
	}
	p, e = r.Read(ctx, "alina", id, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(p)
	for _, word := range []string{"salary", "parameters", "prompt", "token", "justification"} {
		if strings.Contains(string(raw), word) {
			t.Fatal("unsafe projection", word)
		}
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE users SET enabled=false WHERE id='alina'`); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(ctx, "alina", id, 0, 1); e != execution.ErrMissing {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE users SET enabled=true,team_id='payments' WHERE id='alina'`); e != nil {
		t.Fatal(e)
	}
	// Stable history pages, and active routes are found independently of recency.
	for i := 0; i < 3; i++ {
		other := run
		other.ID = actions.ID()
		other.State = "completed"
		other.StartedAt = now.Add(time.Duration(i+1) * time.Second)
		if e = r.Create(ctx, other); e != nil {
			t.Fatal(e)
		}
	}
	list, e := s.Executions(ctx, "alina", "", 1, false)
	if e != nil || len(list.Items) != 1 || list.NextCursor == "" {
		t.Fatal(list, e)
	}
	next, e := s.Executions(ctx, "alina", list.NextCursor, 1, false)
	if e != nil || len(next.Items) != 1 || next.Items[0].ID == list.Items[0].ID {
		t.Fatal(next, e)
	}
	active, e := s.Executions(ctx, "alina", "", 100, true)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range active.Items {
		if !execution.Active(item.State) && item.State != "waiting_user" && item.State != "manual_review" {
			t.Fatal("inactive in active list")
		}
	}
	for _, cursor := range []string{"!", "e30", "bm90LWpzb24"} {
		if _, e = s.Executions(ctx, "alina", cursor, 20, false); e != execution.ErrInvalid {
			t.Fatal(cursor, e)
		}
	}
	if _, e = s.Executions(ctx, "alina", "", 0, false); e != execution.ErrInvalid {
		t.Fatal(e)
	}
	// No migration backfill: legacy jobs remain without route links.
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM execution_links WHERE run_id=$1`, id).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("repeat migration", e)
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = linkExecution(ctx, tx, id, "alina", execution.Link{Kind: "job", ID: actions.ID()}); e != execution.ErrMissing {
		t.Fatal("foreign link", e)
	}
	_ = tx.Rollback(ctx)
	// A business write and its mandatory fact roll back together.
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `UPDATE execution_runs SET detail_complete=false WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	fact.At = now
	fact.Key = "rolled-back"
	if e = appendExecution(ctx, tx, id, fact); e != nil {
		t.Fatal(e)
	}
	_ = tx.Rollback(ctx)
	p, e = r.Read(ctx, "alina", id, 0, 100)
	if e != nil || p.Snapshot.LastSequence != 2 || !p.DetailComplete {
		t.Fatal(p, e)
	}
}

func TestExecutionMigrationPreservesLegacy(t *testing.T) {
	base := testStore(t)
	ctx := context.Background()
	name := "legacy_" + strings.ReplaceAll(actions.ID(), "-", "")
	schema := pgx.Identifier{name}.Sanitize()
	if _, e := base.Pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = base.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	parsed, e := url.Parse(base.Pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	q := parsed.Query()
	q.Set("search_path", name)
	parsed.RawQuery = q.Encode()
	s, e := Open(ctx, parsed.String())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Pool.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"001_identity.sql", "002_actions.sql", "003_runtime.sql", "004_operational_metrics.sql"} {
		raw, e := migrations.ReadFile("migrations/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Seed(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO conversations(user_id) VALUES('alina');INSERT INTO messages(user_id,sequence,role,kind,text,created_at) VALUES('alina',1,'user','text','Демонстрационный старый вопрос',now())`); e != nil {
		t.Fatal(e)
	}
	job := actions.ID()
	if _, e = s.Pool.Exec(ctx, `INSERT INTO jobs(id,user_id,message_id,kind,state,available_at,created_at) SELECT $1,'alina',id,'message','done',now(),now() FROM messages`, job); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"execution_runs", "execution_links", "execution_events"} {
		var count int
		if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 0 {
			t.Fatal("invented legacy routes", table, count, e)
		}
	}
	messages, e := s.Messages(ctx, "alina", 0)
	if e != nil || len(messages) != 1 || len(messages[0].ExecutionIDs) != 0 {
		t.Fatal(messages, e)
	}
	old, e := s.Job(ctx, "alina", job)
	if e != nil || len(old.ExecutionIDs) != 0 || old.State != "completed" {
		t.Fatal(old, e)
	}
}
