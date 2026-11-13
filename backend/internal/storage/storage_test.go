package storage

import (
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_API_DATABASE_URL")
	if url == "" {
		t.Skip("run make integration-test or make coverage for PostgreSQL checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	s.OperationalClock = clock.Fixed{Instant: time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestMigrationSeedAndIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE plan_steps SET status='in_progress' WHERE id='alina-P5'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx)
	if err != nil || len(profiles) != 7 {
		t.Fatal(profiles, err)
	}
	alina, err := s.Profile(ctx, "alina")
	if err != nil || alina.Role != "intern" || *alina.ManagerID != "olga" || alina.StartDate != "2026-11-09" {
		t.Fatal(alina, err)
	}
	if _, err := s.Profile(ctx, "missing"); !IsMissing(err) {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	teams := map[string]bool{}
	for _, p := range profiles {
		roles[p.Role] = true
		if p.TeamID != nil {
			teams[*p.TeamID] = true
		}
	}
	if len(roles) != 4 || len(teams) != 2 {
		t.Fatal(roles, teams)
	}
	own, err := s.Steps(ctx, "igor")
	if err != nil || len(own) != 3 {
		t.Fatal(own, err)
	}
	for _, step := range own {
		if step.OwnerID != "igor" {
			t.Fatal("foreign assignment", step)
		}
	}
	steps, err := s.Steps(ctx, "alina")
	if err != nil || len(steps) != 11 {
		t.Fatal(steps, err)
	}
	for _, step := range steps {
		if step.InternID != "alina" {
			t.Fatal("foreign plan")
		}
		if step.Code == "P5" && step.Status != "in_progress" {
			t.Fatal("seed reset progress")
		}
	}
	var documents, resources int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM resources`).Scan(&resources); err != nil {
		t.Fatal(err)
	}
	if documents != 8 || resources != 11 {
		t.Fatal(documents, resources)
	}
	mockURL := os.Getenv("TEST_MOCKS_DATABASE_URL")
	mocks, err := pgxpool.New(ctx, mockURL)
	if err != nil {
		t.Fatal(err)
	}
	defer mocks.Close()
	if err := mocks.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := s.Pool.QueryRow(ctx, `SELECT has_database_privilege(current_user,'buddy_mocks','CONNECT')`).Scan(&allowed); err != nil || allowed {
		t.Fatal("API can connect to mocks", err)
	}
	if err := mocks.QueryRow(ctx, `SELECT has_database_privilege(current_user,'buddy_api','CONNECT')`).Scan(&allowed); err != nil || allowed {
		t.Fatal("mocks can connect to API", err)
	}
	if _, err := mocks.Exec(ctx, `SELECT * FROM users`); err == nil {
		t.Fatal("mock database contains assistant tables")
	}
}
func TestOpenRejectsConfiguration(t *testing.T) {
	if _, err := Open(context.Background(), "postgres://%zz"); err == nil || err.Error() != "database configuration rejected" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, "postgres://localhost:1/missing"); err == nil || err.Error() != "database unavailable" {
		t.Fatal(err)
	}
}
func TestClosedStore(t *testing.T) {
	s := testStore(t)
	s.Close()
	ctx := context.Background()
	if s.Migrate(ctx) == nil || s.Seed(ctx) == nil {
		t.Fatal("closed database accepted write")
	}
	if _, err := s.Profiles(ctx); err == nil {
		t.Fatal("profiles accepted closed database")
	}
	if _, err := s.Steps(ctx, "alina"); err == nil {
		t.Fatal("steps accepted closed database")
	}
}

func TestSessionStorage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	if err := s.ReplaceSession(ctx, "", "hash-a", "alina", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p, err := s.SessionProfile(ctx, "hash-a", now)
	if err != nil || p.ID != "alina" {
		t.Fatal(p, err)
	}
	if _, err := s.SessionProfile(ctx, "hash-a", now.Add(time.Hour)); !IsMissing(err) {
		t.Fatal("expired session", err)
	}
	if err := s.ReplaceSession(ctx, "hash-a", "hash-b", "maxim", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionProfile(ctx, "hash-a", now); !IsMissing(err) {
		t.Fatal("old token survived", err)
	}
	if err := s.DeleteSession(ctx, "hash-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionProfile(ctx, "hash-b", now); !IsMissing(err) {
		t.Fatal("logout failed", err)
	}
	s.Close()
	if s.ReplaceSession(ctx, "", "hash-c", "alina", now) == nil || s.DeleteSession(ctx, "hash-c") == nil {
		t.Fatal("closed database")
	}
}

func TestAuthorizedCatalog(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"alina", "maxim"} {
		p, err := s.Profile(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		documents, err := s.Documents(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range documents {
			if d.TeamID != nil && *d.TeamID != *p.TeamID {
				t.Fatal("foreign material", d)
			}
		}
		resources, err := s.Resources(ctx, p)
		if err != nil || len(resources) != 10 {
			t.Fatal(resources, err)
		}
		for _, r := range resources {
			if r.Kind == "hidden" || r.ID == "salary-reports" {
				t.Fatal("hidden resource disclosed")
			}
		}
		p.Enabled = false
		if docs, err := s.Documents(ctx, p); err != nil || len(docs) != 0 {
			t.Fatal(docs, err)
		}
		if resources, err := s.Resources(ctx, p); err != nil || len(resources) != 0 {
			t.Fatal(resources, err)
		}
	}
	s.Close()
	p := demo.Profiles()[0]
	if _, err := s.Documents(ctx, p); err == nil {
		t.Fatal("closed database")
	}
	if _, err := s.Resources(ctx, p); err == nil {
		t.Fatal("closed database")
	}
}

func TestPolicyVersionTracksAuthoritativeChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	before, err := s.Profile(ctx, "alina")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET team_id=NULL WHERE id='alina'`); err != nil {
		t.Fatal(err)
	}
	after, err := s.Profile(ctx, "alina")
	if err != nil || after.PolicyVersion <= before.PolicyVersion {
		t.Fatal("identity change did not invalidate policy", after, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET team_id='payments' WHERE id='alina'`); err != nil {
		t.Fatal(err)
	}
	before, err = s.Profile(ctx, "alina")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE resources SET name=name WHERE id='monitoring'`); err != nil {
		t.Fatal(err)
	}
	after, err = s.Profile(ctx, "alina")
	if err != nil || after.PolicyVersion <= before.PolicyVersion {
		t.Fatal("catalogue policy did not invalidate identity scope", after, err)
	}
}
