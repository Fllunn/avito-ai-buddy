package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

const attemptColumns = `a.operational_created_at,a.id::text,a.user_id,a.card_id::text,a.card_version,a.number,a.operation_key::text,a.parameters,a.state,a.conflict_key,a.start_date::text,a.end_date::text,a.may_have_sent,a.reconciliation_count,a.next_reconciliation_at,a.result,a.created_at,a.updated_at,COALESCE((SELECT j.id::text FROM jobs j WHERE j.attempt_id=a.id ORDER BY j.sequence DESC LIMIT 1),'')`

func scanAttempt(row pgx.Row) (actions.Attempt, error) {
	var a actions.Attempt
	err := row.Scan(&a.OperationalCreatedAt, &a.ID, &a.UserID, &a.CardID, &a.CardVersion, &a.Number, &a.OperationKey, &a.Parameters, &a.State, &a.ConflictKey, &a.StartDate, &a.EndDate, &a.MayHaveSent, &a.ReconciliationCount, &a.NextCheckAt, &a.Result, &a.CreatedAt, &a.UpdatedAt, &a.JobID)
	if err == pgx.ErrNoRows {
		return a, actions.ErrMissing
	}
	return a, err
}

type ValidateCard func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error)

// Confirm persists the immutable attempt and its job before any service mutation.
// External preflight reads happen before the short user lock; the service and
// worker repeat business validation immediately before processing.
func (s *Store) ConfirmCard(ctx context.Context, user, id string, expected int, now time.Time, location *time.Location, validate ValidateCard) (actions.Attempt, error) {
	profile, err := s.Profile(ctx, user)
	if err != nil {
		return actions.Attempt{}, actions.ErrMissing
	}
	if err := authz.ServiceAction(profile, now, location); err != nil {
		return actions.Attempt{}, err
	}
	card, err := s.Card(ctx, user, id, now)
	if err != nil {
		return actions.Attempt{}, err
	}
	if card.Version != expected {
		return actions.Attempt{}, actions.ErrConflict
	}
	var reservation actions.Reservation
	if card.State == "draft" {
		if validate == nil {
			return actions.Attempt{}, actions.ErrInvalid
		}
		reservation, err = validate(ctx, profile, card)
		if err != nil {
			return actions.Attempt{}, err
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return actions.Attempt{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var role, start string
	var enabled bool
	var policyVersion int64
	if err := tx.QueryRow(ctx, `SELECT role,to_char(start_date,'YYYY-MM-DD'),enabled,policy_version FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&role, &start, &enabled, &policyVersion); err != nil {
		return actions.Attempt{}, err
	}
	profile.Role = role
	profile.StartDate = start
	profile.Enabled = enabled
	if err := authz.ServiceAction(profile, now, location); err != nil {
		return actions.Attempt{}, err
	}
	if profile.PolicyVersion != policyVersion {
		return actions.Attempt{}, authz.ErrNotAllowed
	}
	var version int
	if err := tx.QueryRow(ctx, `SELECT version FROM cards WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&version); err != nil {
		return actions.Attempt{}, actions.ErrMissing
	}
	card, err = scanCard(tx.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards c JOIN card_versions v ON v.card_id=c.id AND v.version=c.version WHERE c.id=$1 AND c.user_id=$2`, id, user))
	if err != nil {
		return actions.Attempt{}, err
	}
	if card.Version != expected {
		return actions.Attempt{}, actions.ErrConflict
	}
	if card.State == "confirmed" {
		existing, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM attempts a WHERE a.card_id=$1 AND a.card_version=$2 ORDER BY a.number DESC LIMIT 1`, id, expected))
		if err != nil {
			return existing, err
		}
		return existing, tx.Commit(ctx)
	}
	if card.State == "expired" || !now.Before(card.ExpiresAt) {
		return actions.Attempt{}, actions.ErrExpired
	}
	if card.State != "draft" {
		return actions.Attempt{}, actions.ErrConflict
	}
	var conflict bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE user_id=$1 AND state IN ('queued','sending','unavailable','unknown','manual_review') AND (($2<>'' AND conflict_key=$2) OR ($3::date IS NOT NULL AND start_date<=$4::date AND end_date>=$3::date)))`, user, reservation.Key, reservation.StartDate, reservation.EndDate).Scan(&conflict); err != nil {
		return actions.Attempt{}, err
	}
	if conflict {
		return actions.Attempt{}, actions.ErrActive
	}
	operationalNow := s.OperationalClock.Now()
	attempt := actions.Attempt{OperationalCreatedAt: operationalNow, ID: actions.ID(), UserID: user, CardID: id, CardVersion: expected, Number: 1, OperationKey: actions.ID(), Parameters: card.Parameters, State: "queued", ConflictKey: reservation.Key, StartDate: reservation.StartDate, EndDate: reservation.EndDate, CreatedAt: now, UpdatedAt: now, JobID: actions.ID()}
	if _, err = tx.Exec(ctx, `INSERT INTO attempts(id,user_id,card_id,card_version,number,operation_key,parameters,state,conflict_key,start_date,end_date,created_at,updated_at,operational_created_at) VALUES($1,$2,$3,$4,1,$5,$6,'queued',$7,$8::date,$9::date,$10,$10,$11)`, attempt.ID, user, id, expected, attempt.OperationKey, attempt.Parameters, reservation.Key, reservation.StartDate, reservation.EndDate, now, operationalNow); err != nil {
		return actions.Attempt{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,user_id,attempt_id,kind,state,available_at,created_at) VALUES($1,$2,$3,'send','queued',$4,$4)`, attempt.JobID, user, attempt.ID, operationalNow); err != nil {
		return actions.Attempt{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE cards SET state='confirmed',updated_at=$2 WHERE id=$1`, id, now); err != nil {
		return actions.Attempt{}, err
	}
	if err = recordEvent(ctx, tx, user, "attempt:"+attempt.ID+":started", "attempt_started", card.Type, "queued", 0, now); err != nil {
		return actions.Attempt{}, err
	}
	if err = recordEvent(ctx, tx, user, "attempt:"+attempt.ID+":confirmed", "card_confirmed", card.Type, "confirmed", 0, now); err != nil {
		return actions.Attempt{}, err
	}
	if err = s.confirmExecution(ctx, tx, card, attempt); err != nil {
		return actions.Attempt{}, err
	}
	return attempt, tx.Commit(ctx)
}
func (s *Store) Attempt(ctx context.Context, user, id string) (actions.Attempt, error) {
	if !actions.ValidID(id) {
		return actions.Attempt{}, actions.ErrMissing
	}
	return scanAttempt(s.Pool.QueryRow(ctx, `SELECT `+attemptColumns+` FROM attempts a WHERE a.id=$1 AND a.user_id=$2`, id, user))
}
