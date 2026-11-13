package service

import (
	"ai-buddy/backend/platform/demo"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("mock database configuration rejected")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("mock database unavailable")
	}
	return &Store{pool}, nil
}
func (s *Store) Close() { s.Pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(28112641);CREATE TABLE IF NOT EXISTS mock_schema_migrations(name text PRIMARY KEY)`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mock_schema_migrations WHERE name=$1)`, entry.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO mock_schema_migrations(name) VALUES($1)`, entry.Name()); err != nil {
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(28112642)`); err != nil {
		return err
	}
	for _, p := range demo.Profiles() {
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO actors(id,payload,enabled) VALUES($1,$2,true) ON CONFLICT DO NOTHING`, p.ID, data); err != nil {
			return err
		}
	}
	for _, r := range demo.Resources() {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO resources(id,payload) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.ID, data); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO access_requests(id,user_id,resource_id,level,justification,approver_id,status,version,created_at,updated_at) VALUES('ACC-1001','maxim','space-search','read','Для знакомства с командой','anna','approved',3,'2026-11-06T07:00:00Z','2026-11-09T07:00:00Z') ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO grants(user_id,resource_id,level,granted_at,source_request_id) VALUES('maxim','space-search','read','2026-11-09T07:00:00Z','ACC-1001') ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Actor(ctx context.Context, id string) (demo.Profile, error) {
	var data []byte
	var enabled bool
	if err := s.Pool.QueryRow(ctx, `SELECT payload,enabled FROM actors WHERE id=$1`, id).Scan(&data, &enabled); err != nil {
		return demo.Profile{}, err
	}
	var p demo.Profile
	if json.Unmarshal(data, &p) != nil || p.ID != id {
		return p, fmt.Errorf("mock actor schema invalid")
	}
	p.Enabled = enabled
	return p, nil
}

type AccessRequest struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	ResourceID    string    `json:"resource_id"`
	Level         string    `json:"level"`
	Justification string    `json:"justification"`
	ApproverID    string    `json:"approver_id"`
	Status        string    `json:"status"`
	Reason        *string   `json:"reason"`
	Version       int64     `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Grant struct {
	ResourceID      string    `json:"resource_id"`
	Level           string    `json:"level"`
	GrantedAt       time.Time `json:"granted_at"`
	SourceRequestID string    `json:"source_request_id"`
}
type Resource struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Aliases            []string `json:"aliases"`
	CanRequest         bool     `json:"can_request"`
	Levels             []string `json:"levels"`
	DefaultLevel       *string  `json:"default_level"`
	RecommendedTeamIDs []string `json:"recommended_team_ids"`
	RequiresTask       bool     `json:"requires_task"`
	ApproverID         *string  `json:"approver_id"`
}
type APIError struct {
	Code      string            `json:"code"`
	Fields    map[string]string `json:"fields"`
	Retryable bool              `json:"retryable"`
}
type ErrorBody struct {
	Error      APIError `json:"error"`
	ExistingID string   `json:"existing_id,omitempty"`
}

func problem(code string) ErrorBody {
	return ErrorBody{Error: APIError{Code: code, Fields: map[string]string{}, Retryable: code == "SERVICE_UNAVAILABLE"}}
}
