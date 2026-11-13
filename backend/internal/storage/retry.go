package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Store) ReconcileNow(ctx context.Context, user, id string, now time.Time) (string, error) {
	if !actions.ValidID(id) {
		return "", actions.ErrMissing
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&enabled); err != nil || !enabled {
		return "", actions.ErrMissing
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM attempts WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&state); err != nil {
		if err == pgx.ErrNoRows {
			return "", actions.ErrMissing
		}
		return "", err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id::text FROM jobs WHERE attempt_id=$1 AND state IN ('queued','leased')`, id).Scan(&existing)
	if err == nil {
		return existing, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	if state != "unknown" && state != "manual_review" {
		return "", actions.ErrConflict
	}
	jobID := actions.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,user_id,attempt_id,kind,state,available_at,created_at) VALUES($1,$2,$3,'reconcile','queued',$4,$4)`, jobID, user, id, s.OperationalClock.Now()); err != nil {
		return "", err
	}
	return jobID, tx.Commit(ctx)
}
func (s *Store) Retry(ctx context.Context, user, id string, now time.Time, location *time.Location, validate ValidateCard) (actions.Attempt, error) {
	profile, err := s.Profile(ctx, user)
	if err != nil {
		return actions.Attempt{}, actions.ErrMissing
	}
	if err := authz.ServiceAction(profile, now, location); err != nil {
		return actions.Attempt{}, err
	}
	requested, err := s.Attempt(ctx, user, id)
	if err != nil {
		return requested, err
	}
	card, err := s.Card(ctx, user, requested.CardID, now)
	if err != nil {
		return actions.Attempt{}, err
	}
	var reservation actions.Reservation
	if requested.State == "unavailable" || requested.State == "not_created" {
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
	var version int64
	if err := tx.QueryRow(ctx, `SELECT role,to_char(start_date,'YYYY-MM-DD'),enabled,policy_version FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&role, &start, &enabled, &version); err != nil {
		return actions.Attempt{}, err
	}
	if !enabled || role != "intern" || profile.PolicyVersion != version {
		return actions.Attempt{}, authz.ErrNotAllowed
	}
	if now.In(location).Format(time.DateOnly) < start {
		return actions.Attempt{}, authz.ErrBeforeStart
	}
	latest, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM attempts a WHERE card_id=$1 AND card_version=$2 ORDER BY number DESC LIMIT 1`, requested.CardID, requested.CardVersion))
	if err != nil {
		return latest, err
	}
	if latest.ID != requested.ID || latest.State == "queued" || latest.State == "sending" {
		return latest, tx.Commit(ctx)
	}
	if latest.State != "unavailable" && latest.State != "not_created" {
		return latest, actions.ErrConflict
	}
	var expires time.Time
	var cardVersion int
	if err := tx.QueryRow(ctx, `SELECT version,expires_at FROM cards WHERE id=$1 AND user_id=$2 FOR UPDATE`, card.ID, user).Scan(&cardVersion, &expires); err != nil {
		return actions.Attempt{}, err
	}
	if cardVersion != requested.CardVersion {
		return actions.Attempt{}, actions.ErrConflict
	}
	if !now.Before(expires) {
		return actions.Attempt{}, actions.ErrExpired
	}
	var conflict bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE user_id=$1 AND id<>$2 AND state IN ('queued','sending','unavailable','unknown','manual_review') AND (($3<>'' AND conflict_key=$3) OR ($4::date IS NOT NULL AND start_date<=$5::date AND end_date>=$4::date)))`, user, latest.ID, reservation.Key, reservation.StartDate, reservation.EndDate).Scan(&conflict); err != nil {
		return actions.Attempt{}, err
	}
	if conflict {
		return actions.Attempt{}, actions.ErrActive
	}
	if latest.State == "not_created" {
		latest.ID = actions.ID()
		latest.OperationKey = actions.ID()
		latest.Number++
		latest.CreatedAt = now
		latest.OperationalCreatedAt = s.OperationalClock.Now()
		latest.ReconciliationCount = 0
		latest.MayHaveSent = false
		latest.Result = nil
		latest.NextCheckAt = nil
		if _, err = tx.Exec(ctx, `INSERT INTO attempts(id,user_id,card_id,card_version,number,operation_key,parameters,state,conflict_key,start_date,end_date,created_at,updated_at,operational_created_at) VALUES($1,$2,$3,$4,$5,$6,$7,'queued',$8,$9::date,$10::date,$11,$11,$12)`, latest.ID, user, latest.CardID, latest.CardVersion, latest.Number, latest.OperationKey, latest.Parameters, reservation.Key, reservation.StartDate, reservation.EndDate, now, latest.OperationalCreatedAt); err != nil {
			return actions.Attempt{}, err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE attempts SET state='queued',may_have_sent=false,result=NULL,result_source=NULL,updated_at=$2 WHERE id=$1`, latest.ID, now); err != nil {
			return actions.Attempt{}, err
		}
		latest.Result = nil
	}
	latest.State = "queued"
	latest.UpdatedAt = now
	latest.JobID = actions.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,user_id,attempt_id,kind,state,available_at,created_at) VALUES($1,$2,$3,'send','queued',$4,$4)`, latest.JobID, user, latest.ID, s.OperationalClock.Now()); err != nil {
		return actions.Attempt{}, err
	}
	return latest, tx.Commit(ctx)
}
