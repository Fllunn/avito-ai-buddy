package storage

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

// ReplaceSession rotates the previous browser identity atomically. Only hashes
// reach storage; the token itself belongs to the HttpOnly cookie.
func (s *Store) ReplaceSession(ctx context.Context, oldHash, newHash, user string, expires time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, oldHash); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) SELECT $1,id,$3 FROM users WHERE id=$2 AND enabled`, newHash, user, expires)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("session identity unavailable")
	}
	return tx.Commit(ctx)
}
func (s *Store) SessionProfile(ctx context.Context, hash string, now time.Time) (demo.Profile, error) {
	var user string
	if err := s.Pool.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND expires_at>$2`, hash, now).Scan(&user); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return demo.Profile{}, authz.ErrUnauthenticated
		}
		return demo.Profile{}, err
	}
	p, err := s.Profile(ctx, user)
	if errors.Is(err, authz.ErrNotFound) {
		return demo.Profile{}, authz.ErrUnauthenticated
	}
	return p, err
}
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash)
	return err
}
