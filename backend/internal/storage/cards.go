package storage

import (
	"ai-buddy/backend/internal/actions"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"time"
)

const cardColumns = `c.id::text,c.user_id,c.type,c.version,c.state,c.expires_at,c.updated_at,v.parameters`

func scanCard(row pgx.Row) (actions.Card, error) {
	var c actions.Card
	err := row.Scan(&c.ID, &c.UserID, &c.Type, &c.Version, &c.State, &c.ExpiresAt, &c.UpdatedAt, &c.Parameters)
	if err == pgx.ErrNoRows {
		return c, actions.ErrMissing
	}
	return c, err
}
func validCard(kind string, parameters json.RawMessage, ttl time.Duration) bool {
	return (kind == "access_create" || kind == "access_cancel" || kind == "time_off_create" || kind == "time_off_cancel") && json.Valid(parameters) && len(parameters) <= 8192 && ttl > 0
}
func (s *Store) CreateCard(ctx context.Context, user, kind string, parameters json.RawMessage, now time.Time, ttl time.Duration) (actions.Card, error) {
	if !validCard(kind, parameters, ttl) {
		return actions.Card{}, actions.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return actions.Card{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&enabled); err != nil || !enabled {
		return actions.Card{}, actions.ErrMissing
	}
	if _, err = tx.Exec(ctx, `UPDATE cards SET state=CASE WHEN expires_at<=$3 THEN 'expired' ELSE 'replaced' END,updated_at=$3 WHERE user_id=$1 AND type=$2 AND state='draft'`, user, kind, now); err != nil {
		return actions.Card{}, err
	}
	card := actions.Card{ID: actions.ID(), UserID: user, Type: kind, Version: 1, State: "draft", ExpiresAt: now.Add(ttl), UpdatedAt: now, Parameters: parameters}
	if _, err = tx.Exec(ctx, `INSERT INTO cards(id,user_id,type,version,state,expires_at,updated_at) VALUES($1,$2,$3,1,'draft',$4,$5)`, card.ID, user, kind, card.ExpiresAt, now); err != nil {
		return actions.Card{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO card_versions(card_id,version,parameters,expires_at,created_at) VALUES($1,1,$2,$3,$4)`, card.ID, parameters, card.ExpiresAt, now); err != nil {
		return actions.Card{}, err
	}
	return card, tx.Commit(ctx)
}
func (s *Store) Card(ctx context.Context, user, id string, now time.Time) (actions.Card, error) {
	if !actions.ValidID(id) {
		return actions.Card{}, actions.ErrMissing
	}
	card, err := scanCard(s.Pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards c JOIN card_versions v ON v.card_id=c.id AND v.version=c.version WHERE c.id=$1 AND c.user_id=$2`, id, user))
	if card.State == "draft" && !now.Before(card.ExpiresAt) {
		card.State = "expired"
	}
	return card, err
}
func (s *Store) EditCard(ctx context.Context, user, id string, expected int, parameters json.RawMessage, now time.Time, ttl time.Duration) (actions.Card, error) {
	return s.changeCard(ctx, user, id, expected, parameters, now, ttl, false)
}
func (s *Store) CancelCard(ctx context.Context, user, id string, expected int, now time.Time) (actions.Card, error) {
	return s.changeCard(ctx, user, id, expected, nil, now, time.Hour, true)
}
func (s *Store) changeCard(ctx context.Context, user, id string, expected int, parameters json.RawMessage, now time.Time, ttl time.Duration, cancel bool) (actions.Card, error) {
	if !actions.ValidID(id) {
		return actions.Card{}, actions.ErrMissing
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return actions.Card{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Lock the parent before joining the version. A joined SELECT FOR UPDATE
	// can recheck a new parent against an old child after a concurrent edit.
	var lockedVersion int
	if err := tx.QueryRow(ctx, `SELECT version FROM cards WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&lockedVersion); err != nil {
		if err == pgx.ErrNoRows {
			return actions.Card{}, actions.ErrMissing
		}
		return actions.Card{}, err
	}
	card, err := scanCard(tx.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards c JOIN card_versions v ON v.card_id=c.id AND v.version=c.version WHERE c.id=$1 AND c.user_id=$2`, id, user))
	if err != nil {
		return card, err
	}
	if card.Version != expected {
		return card, actions.ErrConflict
	}
	if cancel && card.State == "discarded" {
		return card, tx.Commit(ctx)
	}
	if !now.Before(card.ExpiresAt) {
		return card, actions.ErrExpired
	}
	if card.State != "draft" {
		return card, actions.ErrConflict
	}
	card.UpdatedAt = now
	if cancel {
		card.State = "discarded"
		_, err = tx.Exec(ctx, `UPDATE cards SET state='discarded',updated_at=$2 WHERE id=$1`, id, now)
	} else {
		if !validCard(card.Type, parameters, ttl) {
			return card, actions.ErrInvalid
		}
		card.Version++
		card.Parameters = parameters
		card.ExpiresAt = now.Add(ttl)
		if _, err = tx.Exec(ctx, `INSERT INTO card_versions(card_id,version,parameters,expires_at,created_at) VALUES($1,$2,$3,$4,$5)`, id, card.Version, parameters, card.ExpiresAt, now); err != nil {
			return card, err
		}
		_, err = tx.Exec(ctx, `UPDATE cards SET version=$2,expires_at=$3,updated_at=$4 WHERE id=$1`, id, card.Version, card.ExpiresAt, now)
	}
	if err != nil {
		return card, err
	}
	return card, tx.Commit(ctx)
}
