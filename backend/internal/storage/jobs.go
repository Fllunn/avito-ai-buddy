package storage

import (
	"ai-buddy/backend/internal/actions"
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Store) Claim(ctx context.Context, now time.Time, lease time.Duration) (actions.Work, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return actions.Work{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var job actions.Work
	var attemptID string
	err = tx.QueryRow(ctx, `SELECT id::text,attempt_id::text,kind FROM jobs WHERE attempt_id IS NOT NULL AND ((state='queued' AND available_at<=$1) OR (state='leased' AND lease_until<=$1)) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&job.JobID, &attemptID, &job.Kind)
	if err == pgx.ErrNoRows {
		return job, actions.ErrNoWork
	}
	if err != nil {
		return job, err
	}
	job.Attempt, err = scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM attempts a WHERE a.id=$1`, attemptID))
	if err != nil {
		return job, err
	}
	if err = tx.QueryRow(ctx, `SELECT type FROM cards WHERE id=$1`, job.Attempt.CardID).Scan(&job.CardType); err != nil {
		return job, err
	}
	if job.Attempt.MayHaveSent {
		job.Kind = "reconcile"
	}
	job.LeaseToken = actions.ID()
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state='leased',kind=$2,lease_token=$3,lease_until=$4 WHERE id=$1`, job.JobID, job.Kind, job.LeaseToken, now.Add(lease)); err != nil {
		return job, err
	}
	return job, tx.Commit(ctx)
}
func lockWork(ctx context.Context, tx pgx.Tx, job actions.Work) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT state='leased' AND lease_token=$2::uuid FROM jobs WHERE id=$1 FOR UPDATE`, job.JobID, job.LeaseToken).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return actions.ErrLeaseLost
	}
	return nil
}
func (s *Store) MarkSending(ctx context.Context, job actions.Work, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := lockWork(ctx, tx, job); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state='sending',may_have_sent=true,updated_at=$2 WHERE id=$1`, job.Attempt.ID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Uncertain(ctx context.Context, job actions.Work, now, next time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := lockWork(ctx, tx, job); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state='unknown',may_have_sent=true,next_reconciliation_at=$2,updated_at=$3 WHERE id=$1`, job.Attempt.ID, next, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state='queued',kind='reconcile',available_at=$2,lease_token=NULL,lease_until=NULL WHERE id=$1`, job.JobID, next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ReconcileLater(ctx context.Context, job actions.Work, now, operationalNow time.Time, delays []time.Duration) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := lockWork(ctx, tx, job); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT reconciliation_count FROM attempts WHERE id=$1 FOR UPDATE`, job.Attempt.ID).Scan(&count); err != nil {
		return err
	}
	count++
	state, jobState := "unknown", "queued"
	var next *time.Time
	if count >= len(delays) {
		state = "manual_review"
		jobState = "done"
	} else {
		instant := operationalNow.Add(delays[count])
		next = &instant
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state=$2,reconciliation_count=$3,next_reconciliation_at=$4,updated_at=$5 WHERE id=$1`, job.Attempt.ID, state, count, next, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state=$2,kind='reconcile',available_at=COALESCE($3,available_at),lease_token=NULL,lease_until=NULL WHERE id=$1`, job.JobID, jobState, next); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) NotCreated(ctx context.Context, job actions.Work, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := lockWork(ctx, tx, job); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state='not_created',next_reconciliation_at=NULL,updated_at=$2 WHERE id=$1`, job.Attempt.ID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state='done',lease_token=NULL,lease_until=NULL WHERE id=$1`, job.JobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
