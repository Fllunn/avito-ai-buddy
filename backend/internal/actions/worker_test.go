package actions

import (
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var syntheticFailure = errors.New("synthetic")

type workerStore struct {
	profile                                  *demo.Profile
	claimedAt, finishedAt, nextAt            time.Time
	job                                      Work
	claimErr, profileErr, sendErr, finishErr error
	sent, finished, uncertain, later, absent int
	result                                   Result
}

func (s *workerStore) Claim(_ context.Context, now time.Time, _ time.Duration) (Work, error) {
	s.claimedAt = now
	return s.job, s.claimErr
}
func (s *workerStore) Profile(context.Context, string) (demo.Profile, error) {
	if s.profile != nil {
		return *s.profile, s.profileErr
	}
	return demo.Profiles()[0], s.profileErr
}
func (s *workerStore) MarkSending(context.Context, Work, time.Time) error { s.sent++; return s.sendErr }
func (s *workerStore) Finish(_ context.Context, _ Work, r Result, now time.Time) error {
	s.finishedAt = now
	s.finished++
	s.result = r
	return s.finishErr
}
func (s *workerStore) Uncertain(_ context.Context, _ Work, _ time.Time, next time.Time) error {
	s.nextAt = next
	s.uncertain++
	return nil
}
func (s *workerStore) ReconcileLater(context.Context, Work, time.Time, time.Time, []time.Duration) error {
	s.later++
	return nil
}
func (s *workerStore) NotCreated(context.Context, Work, time.Time) error { s.absent++; return nil }

type workerClient struct {
	validationErr, reconcileErr     error
	result                          Result
	reply                           Reconciliation
	executed, validated, reconciled int
	wait                            bool
}

func (c *workerClient) Validate(_ context.Context, _ demo.Profile, _ string, parameters json.RawMessage) error {
	c.validated++
	if len(parameters) > 0 {
		parameters[0] = 'x'
	}
	return c.validationErr
}
func (c *workerClient) Execute(ctx context.Context, w Work) Result {
	c.executed++
	if c.wait {
		<-ctx.Done()
	}
	if !json.Valid(w.Attempt.Parameters) {
		panic("validation mutated immutable snapshot")
	}
	return c.result
}
func (c *workerClient) Reconcile(context.Context, Work) (Reconciliation, error) {
	c.reconciled++
	return c.reply, c.reconcileErr
}
func baseWorker(s *workerStore, c *workerClient) Worker {
	s.job = Work{JobID: ID(), LeaseToken: ID(), Kind: "send", CardType: "access_create", Attempt: Attempt{ID: ID(), UserID: "alina", Parameters: json.RawMessage(`{"resource_id":"monitoring"}`)}}
	return Worker{Store: s, Client: c, Timeout: time.Second, Lease: 30 * time.Second, Delays: []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}}
}
func TestWorkerOutcomes(t *testing.T) {
	for _, kind := range []Outcome{Success, Rejected, Unavailable, Unknown, "bad"} {
		t.Run(string(kind), func(t *testing.T) {
			store, client := &workerStore{}, &workerClient{result: Result{Kind: kind, Source: "service"}}
			worker := baseWorker(store, client)
			if err := worker.Once(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if store.sent != 1 || client.executed != 1 || client.validated != 1 {
				t.Fatal(store, client)
			}
			known := kind == Success || kind == Rejected || kind == Unavailable
			if known && store.finished != 1 || !known && store.uncertain != 1 {
				t.Fatal(store)
			}
		})
	}
}
func TestWorkerFailuresBeforeAndAfterSend(t *testing.T) {
	for _, stage := range []string{"claim", "profile", "validation", "mark", "save", "late"} {
		t.Run(stage, func(t *testing.T) {
			store, client := &workerStore{}, &workerClient{result: Result{Kind: Success}}
			worker := baseWorker(store, client)
			switch stage {
			case "claim":
				store.claimErr = ErrNoWork
			case "profile":
				store.profileErr = syntheticFailure
			case "validation":
				client.validationErr = syntheticFailure
			case "mark":
				store.sendErr = syntheticFailure
			case "save":
				store.finishErr = syntheticFailure
			case "late":
				client.wait = true
				worker.Timeout = time.Millisecond
			}
			err := worker.Once(context.Background(), time.Now())
			if stage == "claim" || stage == "mark" {
				if err == nil {
					t.Fatal("error lost")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "profile", "validation":
				if client.executed != 0 || store.sent != 0 || store.result.Source != "local_validation" || store.result.Kind != Rejected {
					t.Fatal(store, client)
				}
			case "claim", "mark":
				if client.executed != 0 {
					t.Fatal("sent without durable marker")
				}
			case "save", "late":
				if store.uncertain != 1 {
					t.Fatal("possibly sent was not reconciled")
				}
			}
		})
	}
}
func TestWorkerReconcilesInsteadOfResending(t *testing.T) {
	for _, state := range []string{"not_created", "completed", "pending", "error", "bad_result", "save_failure"} {
		t.Run(state, func(t *testing.T) {
			store, client := &workerStore{}, &workerClient{}
			worker := baseWorker(store, client)
			store.job.Attempt.MayHaveSent = true
			client.reply = Reconciliation{State: state, Result: Result{Kind: Success}}
			if state == "error" {
				client.reconcileErr = syntheticFailure
			}
			if state == "bad_result" {
				client.reply.State = "completed"
				client.reply.Result.Kind = Unavailable
			}
			if state == "save_failure" {
				client.reply.State = "completed"
				store.finishErr = syntheticFailure
			}
			if err := worker.Once(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if client.executed != 0 || client.validated != 0 || client.reconciled != 1 {
				t.Fatal("restarted job resent", client)
			}
			if state == "not_created" && store.absent != 1 || state == "completed" && store.finished != 1 || state != "not_created" && state != "completed" && store.later != 1 {
				t.Fatal(store)
			}
		})
	}
}

func TestSeparateBusinessAndOperationalClocks(t *testing.T) {
	business := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	operational := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	store, client := &workerStore{}, &workerClient{result: Result{Kind: Success}}
	worker := baseWorker(store, client)
	worker.BusinessClock = clock.Fixed{Instant: business}
	if err := worker.Once(context.Background(), operational); err != nil {
		t.Fatal(err)
	}
	if !store.claimedAt.Equal(operational) || !store.finishedAt.Equal(business) {
		t.Fatal("business freeze changed queue clock", store)
	}
	client.result.Kind = Unknown
	if err := worker.Once(context.Background(), operational); err != nil {
		t.Fatal(err)
	}
	if !store.nextAt.Equal(operational.Add(time.Second)) {
		t.Fatal("reconciliation scheduled against frozen date", store.nextAt)
	}
}
func TestTransientPreflightFailureIsNotAServiceRejection(t *testing.T) {
	store, client := &workerStore{}, &workerClient{validationErr: ValidationError{Retryable: true}}
	worker := baseWorker(store, client)
	if err := worker.Once(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if store.result.Source != "local_validation" || store.result.Kind != Unavailable || client.executed != 0 {
		t.Fatal(store, client)
	}
}

func TestRunnerCancellationAndConfiguration(t *testing.T) {
	store, client := &workerStore{claimErr: ErrNoWork}, &workerClient{}
	worker := baseWorker(store, client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx, clock.System{}, time.Millisecond); err != context.Canceled {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), clock.System{}, 0); err != ErrInvalid {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx, clock.System{}, time.Millisecond); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestWorkerReauthorizesQueuedOwner(t *testing.T) {
	store, client := &workerStore{}, &workerClient{result: Result{Kind: Success}}
	worker := baseWorker(store, client)
	profile := demo.Profiles()[0]
	profile.Role = "buddy"
	store.profile = &profile
	if err := worker.Once(context.Background(), time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if client.executed != 0 || client.validated != 0 || store.result.Source != "local_validation" {
		t.Fatal("queued role change was ignored", store, client)
	}
}
