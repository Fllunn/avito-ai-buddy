package actions

import (
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Outcome string

const (
	Success     Outcome = "success"
	Rejected    Outcome = "rejected"
	Unavailable Outcome = "unavailable"
	Unknown     Outcome = "unknown"
)

type Object struct {
	Service       string    `json:"service"`
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	ResourceID    string    `json:"resource_id,omitempty"`
	Level         string    `json:"level,omitempty"`
	Justification string    `json:"justification,omitempty"`
	StartDate     string    `json:"start_date,omitempty"`
	EndDate       string    `json:"end_date,omitempty"`
	Workdays      int       `json:"workdays,omitempty"`
	ApproverID    string    `json:"approver_id"`
	Status        string    `json:"status"`
	Reason        *string   `json:"reason"`
	Version       int64     `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Result struct {
	Kind   Outcome `json:"kind"`
	Object *Object `json:"object,omitempty"`
	Code   string  `json:"code,omitempty"`
	Source string  `json:"source"`
}
type Reconciliation struct {
	State  string
	Result Result
}
type Work struct {
	JobID      string
	LeaseToken string
	Kind       string
	CardType   string
	Attempt    Attempt
}

var ErrNoWork = errors.New("NO_WORK")
var ErrLeaseLost = errors.New("LEASE_LOST")
var ErrContract = errors.New("SERVICE_CONTRACT_ERROR")

type ValidationError struct{ Retryable bool }

func (e ValidationError) Error() string { return "VALIDATION_FAILED" }

type WorkerStore interface {
	Claim(context.Context, time.Time, time.Duration) (Work, error)
	Profile(context.Context, string) (demo.Profile, error)
	MarkSending(context.Context, Work, time.Time) error
	Finish(context.Context, Work, Result, time.Time) error
	Uncertain(context.Context, Work, time.Time, time.Time) error
	ReconcileLater(context.Context, Work, time.Time, time.Time, []time.Duration) error
	NotCreated(context.Context, Work, time.Time) error
}
type Client interface {
	Validate(context.Context, demo.Profile, string, json.RawMessage) error
	Execute(context.Context, Work) Result
	Reconcile(context.Context, Work) (Reconciliation, error)
}
type Worker struct {
	BusinessClock clock.Clock
	Location      *time.Location
	Store         WorkerStore
	Client        Client
	Timeout       time.Duration
	Lease         time.Duration
	Delays        []time.Duration
}

// Once handles one durable job. Callers supply Clock.Now, keeping demo schedules
// and restart tests independent from the host wall clock.
func (w Worker) Once(ctx context.Context, now time.Time) error {
	if w.Timeout <= 0 || w.Lease <= 0 || len(w.Delays) == 0 {
		return ErrInvalid
	}
	operationalNow := now
	if w.BusinessClock != nil {
		now = w.BusinessClock.Now()
	}
	job, err := w.Store.Claim(ctx, operationalNow, w.Lease)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	if job.Kind == "reconcile" || job.Attempt.MayHaveSent {
		answer, err := w.Client.Reconcile(callCtx, job)
		if err == nil && callCtx.Err() == nil {
			switch answer.State {
			case "not_created":
				return w.Store.NotCreated(ctx, job, now)
			case "completed":
				if answer.Result.Kind == Success || answer.Result.Kind == Rejected {
					if err := w.Store.Finish(ctx, job, answer.Result, now); err == nil {
						return nil
					}
				}
			}
		}
		return w.Store.ReconcileLater(ctx, job, now, operationalNow, w.Delays)
	}
	profile, err := w.Store.Profile(callCtx, job.Attempt.UserID)
	if err == nil {
		location := w.Location
		if location == nil {
			location = time.UTC
		}
		if profile.ID != job.Attempt.UserID {
			err = authz.ErrNotFound
		} else {
			err = authz.ServiceAction(profile, now, location)
		}
	}
	if err == nil {
		err = w.Client.Validate(callCtx, profile, job.CardType, append(json.RawMessage(nil), job.Attempt.Parameters...))
	}
	if err != nil {
		// No request has been sent. This is a local validation failure, never a
		// fabricated business response from the external service.
		kind, code := Rejected, "VALIDATION_FAILED"
		var validation ValidationError
		if errors.As(err, &validation) && validation.Retryable {
			kind = Unavailable
			code = "VALIDATION_UNAVAILABLE"
		}
		return w.Store.Finish(ctx, job, Result{Kind: kind, Code: code, Source: "local_validation"}, now)
	}
	if err := w.Store.MarkSending(ctx, job, now); err != nil {
		return err
	}
	result := w.Client.Execute(callCtx, job)
	if callCtx.Err() != nil {
		result = Result{Kind: Unknown, Source: "service"}
	}
	switch result.Kind {
	case Success, Rejected, Unavailable:
		if err := w.Store.Finish(ctx, job, result, now); err == nil {
			return nil
		}
	}
	return w.Store.Uncertain(ctx, job, now, operationalNow.Add(w.Delays[0]))
}

// Run polls the durable queue using operational time. BusinessClock is only
// consulted for domain validation and user-facing timestamps.
func (w Worker) Run(ctx context.Context, operational clock.Clock, poll time.Duration) error {
	if w.Lease <= 0 || w.Timeout <= 0 || len(w.Delays) == 0 || poll <= 0 {
		return ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := w.Once(ctx, operational.Now())
		if err == nil {
			continue
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
