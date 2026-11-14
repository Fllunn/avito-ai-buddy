package storage

import (
	"ai-buddy/backend/internal/authz"
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	Pool             *pgxpool.Pool
	OperationalClock clock.Clock
}

func Open(ctx context.Context, url string, locations ...*time.Location) (*Store, error) {
	settings, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database configuration rejected")
	}
	if len(locations) > 0 && locations[0] != nil {
		settings.ConnConfig.RuntimeParams["timezone"] = locations[0].String()
	}
	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("database configuration rejected")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unavailable")
	}
	return &Store{Pool: pool, OperationalClock: clock.System{}}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(28112601); CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("prepare migration: %w", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) Seed(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(28112602)`); err != nil {
		return err
	}
	for _, p := range demo.Profiles() {
		if _, err = tx.Exec(ctx, `INSERT INTO users(id,name,role,team_id,start_date,buddy_id,manager_id,curator_id) VALUES($1,$2,$3,$4,$5::date,$6,$7,$8) ON CONFLICT DO NOTHING`, p.ID, p.Name, p.Role, p.TeamID, p.StartDate, p.BuddyID, p.ManagerID, p.CuratorID); err != nil {
			return fmt.Errorf("seed profiles: %w", err)
		}
	}
	for _, step := range demo.Steps() {
		if _, err = tx.Exec(ctx, `INSERT INTO plan_steps(id,intern_id,code,title,period,owner_id,closure,status,dependencies,source,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::timestamptz) ON CONFLICT DO NOTHING`, step.ID, step.InternID, step.Code, step.Title, step.Period, step.OwnerID, step.Closure, step.Status, step.Dependencies, step.Source, step.UpdatedAt); err != nil {
			return fmt.Errorf("seed plan: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO step_assignments(step_id,owner_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, step.ID, step.OwnerID); err != nil {
			return err
		}
	}
	for _, d := range demo.Documents() {
		if _, err = tx.Exec(ctx, `INSERT INTO documents(id,title,team_id,roles,owner_id,url,stale,fragments,demo) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, d.ID, d.Title, d.TeamID, d.Roles, d.OwnerID, d.URL, d.Stale, d.Fragments, d.Demo); err != nil {
			return fmt.Errorf("seed documents: %w", err)
		}
	}
	for _, r := range demo.Resources() {
		if _, err = tx.Exec(ctx, `INSERT INTO resources(id,name,kind,levels,default_level,approver_id,recommended_teams,synonyms) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, r.ID, r.Name, r.Kind, r.Levels, r.DefaultLevel, r.ApproverID, r.RecommendedTeams, r.Synonyms); err != nil {
			return fmt.Errorf("seed resources: %w", err)
		}
	}
	return tx.Commit(ctx)
}

const profileColumns = `id,name,role,team_id,to_char(start_date,'YYYY-MM-DD'),buddy_id,manager_id,curator_id,policy_version,enabled`

func (s *Store) Profile(ctx context.Context, id string) (demo.Profile, error) {
	var p demo.Profile
	err := s.Pool.QueryRow(ctx, `SELECT `+profileColumns+` FROM users WHERE id=$1 AND enabled`, id).Scan(&p.ID, &p.Name, &p.Role, &p.TeamID, &p.StartDate, &p.BuddyID, &p.ManagerID, &p.CuratorID, &p.PolicyVersion, &p.Enabled)
	if err == pgx.ErrNoRows {
		err = authz.ErrNotFound
	}
	return p, err
}
func (s *Store) Profiles(ctx context.Context) ([]demo.Profile, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+profileColumns+` FROM users WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []demo.Profile{}
	for rows.Next() {
		var p demo.Profile
		if err := rows.Scan(&p.ID, &p.Name, &p.Role, &p.TeamID, &p.StartDate, &p.BuddyID, &p.ManagerID, &p.CuratorID, &p.PolicyVersion, &p.Enabled); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) Steps(ctx context.Context, user string) ([]demo.Step, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id,p.intern_id,p.code,p.title,p.period,p.owner_id,p.closure,p.status,p.dependencies,p.source,p.updated_at,p.version FROM plan_steps p JOIN step_assignments a ON a.step_id=p.id WHERE a.owner_id=$1 OR p.intern_id=$1 ORDER BY p.period,p.code`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []demo.Step{}
	for rows.Next() {
		var p demo.Step
		var updated time.Time
		if err := rows.Scan(&p.ID, &p.InternID, &p.Code, &p.Title, &p.Period, &p.OwnerID, &p.Closure, &p.Status, &p.Dependencies, &p.Source, &updated, &p.Version); err != nil {
			return nil, err
		}
		p.UpdatedAt = updated.UTC().Format(time.RFC3339)
		result = append(result, p)
	}
	return result, rows.Err()
}
func IsMissing(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authz.ErrNotFound) || errors.Is(err, authz.ErrUnauthenticated)
}
