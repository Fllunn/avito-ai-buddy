package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/execution"
	"ai-buddy/backend/internal/metrics"
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func jobFixture(t *testing.T, s *Store) (actions.Work, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	s.OperationalClock = clock.Fixed{Instant: now}
	card, err := s.CreateCard(ctx, "alina", "access_create", json.RawMessage(`{"resource_id":"space-payments","level":"read","justification":"Для знакомства с командой"}`), now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
		return actions.Reservation{Key: actions.ID()}, nil
	}
	a, err := s.ConfirmCard(ctx, "alina", card.ID, 1, now, time.UTC, validate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool.Exec(ctx, `UPDATE jobs SET state='done',lease_token=NULL,lease_until=NULL WHERE attempt_id=$1`, a.ID)
	})
	job, err := s.Claim(ctx, now, 30*time.Second)
	if err != nil || job.Attempt.ID != a.ID {
		t.Fatal(job, err)
	}
	return job, now
}
func objectResult(id, status string, version int64, now time.Time) actions.Result {
	return actions.Result{Kind: actions.Success, Source: "service", Object: &actions.Object{Service: "access", ID: id, UserID: "alina", ResourceID: "space-payments", Level: "read", Justification: "Для знакомства с командой", ApproverID: "olga", Status: status, Version: version, CreatedAt: now, UpdatedAt: now.Add(time.Duration(version) * time.Second)}}
}
func TestLeaseExpiryReconcilesPossibleSend(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.MarkSending(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, now.Add(29*time.Second), 30*time.Second); err != actions.ErrNoWork {
		t.Fatal(err)
	}
	restart, err := Open(ctx, s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer restart.Close()
	leased, err := restart.Claim(ctx, now.Add(30*time.Second), 30*time.Second)
	if err != nil || leased.Kind != "reconcile" || !leased.Attempt.MayHaveSent || leased.LeaseToken == job.LeaseToken {
		t.Fatal(leased, err)
	}
	if err := s.Finish(ctx, job, objectResult("ACC-9001", "created", 1, now), now); !errors.Is(err, actions.ErrLeaseLost) {
		t.Fatal("old worker overwrote new lease", err)
	}
	if err := restart.NotCreated(ctx, leased, now.Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	a, err := s.Attempt(ctx, "alina", job.Attempt.ID)
	if err != nil || a.State != "not_created" {
		t.Fatal(a, err)
	}
	assertAttemptRoute(t, s, a.ID, "failed", "not_created")
}
func TestReconciliationScheduleSurvivesRestart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.MarkSending(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	delays := []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}
	if err := s.Uncertain(ctx, job, now, now.Add(delays[0])); err != nil {
		t.Fatal(err)
	}
	next := now.Add(delays[0])
	for count := 1; count <= 5; count++ {
		restarted, err := Open(ctx, s.Pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		a, err := restarted.Attempt(ctx, "alina", job.Attempt.ID)
		if err != nil || a.NextCheckAt == nil || !a.NextCheckAt.Equal(next) {
			t.Fatal(a, err)
		}
		work, err := restarted.Claim(ctx, next, 30*time.Second)
		if err != nil || work.Kind != "reconcile" {
			t.Fatal(work, err)
		}
		if err := restarted.ReconcileLater(ctx, work, now, next, delays); err != nil {
			t.Fatal(err)
		}
		a, err = restarted.Attempt(ctx, "alina", job.Attempt.ID)
		if err != nil || a.ReconciliationCount != count {
			t.Fatal(a, err)
		}
		restarted.Close()
		if count < 5 {
			next = next.Add(delays[count])
		} else if a.State != "manual_review" || a.NextCheckAt != nil {
			t.Fatal(a)
		}
	}
	assertAttemptRoute(t, s, job.Attempt.ID, "manual_review", "manual_review")
	if _, err := s.Claim(ctx, next.Add(24*time.Hour), time.Minute); err != actions.ErrNoWork {
		t.Fatal("manual review continued automatically", err)
	}
}
func TestAtomicProjectionVersionsAndDeduplication(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.Finish(ctx, job, objectResult("ACC-9002", "created", 1, now), now); err != nil {
		t.Fatal(err)
	}
	step := func() string {
		t.Helper()
		var status string
		if err := s.Pool.QueryRow(ctx, `SELECT status FROM plan_steps WHERE id='alina-P6'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if step() != "waiting" {
		t.Fatal("created announced access")
	}
	job, now = jobFixture(t, s)
	approved := objectResult("ACC-9002", "approved", 3, now)
	if err := s.Finish(ctx, job, approved, now); err != nil {
		t.Fatal(err)
	}
	if step() != "done" {
		t.Fatal("approved did not complete P6")
	}
	job, now = jobFixture(t, s)
	if err := s.Finish(ctx, job, objectResult("ACC-9002", "created", 1, now), now); err != nil {
		t.Fatal(err)
	}
	if step() != "done" {
		t.Fatal("late creation rolled plan back")
	}
	var version int64
	var payload []byte
	if err := s.Pool.QueryRow(ctx, `SELECT service_version,payload FROM service_objects WHERE object_id='ACC-9002'`).Scan(&version, &payload); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	job, now = jobFixture(t, s)
	if err := s.Finish(ctx, job, approved, now); err != nil {
		t.Fatal("same version same content", err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE object_id='ACC-9002'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate notifications", count, err)
	}
	job, now = jobFixture(t, s)
	contradictory := objectResult("ACC-9002", "created", 3, now)
	if err := s.Finish(ctx, job, contradictory, now); err != actions.ErrContract {
		t.Fatal("same version contradiction accepted", err)
	}
	if step() != "done" {
		t.Fatal("contract error changed plan")
	}
}
func TestFailedResultTransactionHasNoPartialSuccess(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.MarkSending(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_test_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER fail_test_notification BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION fail_test_notification()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_test_notification ON notifications; DROP FUNCTION IF EXISTS fail_test_notification()`)
	})
	result := objectResult("ACC-9003", "approved", 1, now)
	if err := s.Finish(ctx, job, result, now); err == nil {
		t.Fatal("transaction failure ignored")
	}
	var objects int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM service_objects WHERE object_id='ACC-9003'`).Scan(&objects); err != nil || objects != 0 {
		t.Fatal("partial object", objects, err)
	}
	a, err := s.Attempt(ctx, "alina", job.Attempt.ID)
	if err != nil || a.State != "sending" {
		t.Fatal("false success", a, err)
	}
	if _, err := s.Pool.Exec(ctx, `DROP TRIGGER fail_test_notification ON notifications; DROP FUNCTION fail_test_notification()`); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, job, result, now); err != nil {
		t.Fatal(err)
	}
}
func TestResultSanitizationAndContract(t *testing.T) {
	now := time.Now().UTC()
	result := objectResult("ACC-1", "created", 1, now)
	reason := "Отказ: диагноз диабет"
	result.Object.Reason = &reason
	safe, err := cleanResult(context.Background(), "alina", result)
	if err != nil || safe.Object.Reason == nil || *safe.Object.Reason != "[чувствительное сообщение скрыто]" {
		t.Fatal(safe, err)
	}
	invalid := []actions.Result{{Kind: actions.Success}, {Kind: actions.Unknown}, {Kind: actions.Success, Source: "invented"}}
	for _, r := range invalid {
		if _, err := cleanResult(context.Background(), "alina", r); err != actions.ErrContract {
			t.Fatal(r, err)
		}
	}
	for _, kind := range []actions.Outcome{actions.Rejected, actions.Unavailable} {
		r, err := cleanResult(context.Background(), "alina", actions.Result{Kind: kind, Code: "raw private failure"})
		if err != nil || r.Code != "SERVICE_ERROR" || r.Object != nil {
			t.Fatal(r, err)
		}
	}
	for _, change := range []func(*actions.Object){func(o *actions.Object) { o.UserID = "maxim" }, func(o *actions.Object) { o.ID = "bad" }, func(o *actions.Object) { o.Version = 0 }, func(o *actions.Object) { o.CreatedAt = time.Time{} }, func(o *actions.Object) { o.ResourceID = "" }, func(o *actions.Object) { o.Service = "bad" }, func(o *actions.Object) { o.Status = "bad" }} {
		r := objectResult("ACC-1", "created", 1, now)
		change(r.Object)
		if _, err := cleanResult(context.Background(), "alina", r); err != actions.ErrContract {
			t.Fatal(r, err)
		}
	}
}

func TestRetryUsesSameKeyOnlyForGuaranteedUnavailability(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.Finish(ctx, job, actions.Result{Kind: actions.Unavailable, Code: "SERVICE_UNAVAILABLE", Source: "service"}, now); err != nil {
		t.Fatal(err)
	}
	validate := func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
		return actions.Reservation{Key: job.Attempt.ConflictKey}, nil
	}
	var wg sync.WaitGroup
	outcomes := make(chan actions.Attempt, 2)
	for range 2 {
		wg.Go(func() {
			a, err := s.Retry(ctx, "alina", job.Attempt.ID, now, time.UTC, validate)
			if err != nil {
				t.Error(err)
			}
			outcomes <- a
		})
	}
	wg.Wait()
	close(outcomes)
	var retried actions.Attempt
	for a := range outcomes {
		if a.ID != job.Attempt.ID || a.OperationKey != job.Attempt.OperationKey {
			t.Fatal("unavailable retry changed key")
		}
		if retried.JobID != "" && a.JobID != retried.JobID {
			t.Fatal("retry clicks created multiple jobs")
		}
		retried = a
	}
	a, err := s.Attempt(ctx, "alina", job.Attempt.ID)
	if err != nil || a.JobID != retried.JobID {
		t.Fatal("fixed demo timestamp lost latest job", a, err)
	}
	work, err := s.Claim(ctx, now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.NotCreated(ctx, work, now); err != nil {
		t.Fatal(err)
	}
	next, err := s.Retry(ctx, "alina", work.Attempt.ID, now, time.UTC, validate)
	if err != nil || next.ID == work.Attempt.ID || next.OperationKey == work.Attempt.OperationKey || next.Number != 2 {
		t.Fatal("not_created reused closed key", next, err)
	}
	t.Cleanup(func() { _, _ = s.Pool.Exec(ctx, `UPDATE jobs SET state='done' WHERE attempt_id=$1`, next.ID) })
	again, err := s.Retry(ctx, "alina", work.Attempt.ID, now, time.UTC, validate)
	if err != nil || again.ID != next.ID {
		t.Fatal("old retry created third attempt", again, err)
	}
}
func TestManualReconciliationMergesClicksWithoutResettingLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if _, err := s.ReconcileNow(ctx, "maxim", job.Attempt.ID, now); err != actions.ErrMissing {
		t.Fatal("foreign manual check", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE attempts SET state='manual_review',may_have_sent=true,reconciliation_count=5 WHERE id=$1;`, job.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE jobs SET state='done',lease_token=NULL,lease_until=NULL WHERE id=$1`, job.JobID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			id, err := s.ReconcileNow(ctx, "alina", job.Attempt.ID, now)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first != "" && first != id {
			t.Fatal("manual clicks created duplicate jobs")
		}
		first = id
	}
	work, err := s.Claim(ctx, now, 30*time.Second)
	if err != nil || work.Attempt.ReconciliationCount != 5 || work.Kind != "reconcile" {
		t.Fatal(work, err)
	}
	if err := s.ReconcileLater(ctx, work, now, now, []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Attempt(ctx, "alina", job.Attempt.ID)
	if err != nil || a.State != "manual_review" || a.ReconciliationCount != 6 || a.NextCheckAt != nil {
		t.Fatal("manual click reset auto limit", a, err)
	}
	if _, err := s.Retry(ctx, "alina", job.Attempt.ID, now, time.UTC, accessReservation); err != actions.ErrConflict {
		t.Fatal("conflicting retry after manual review", err)
	}
}
func TestRetryRequiresFreshConfirmationAfterExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, now := jobFixture(t, s)
	if err := s.NotCreated(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, "alina", job.Attempt.ID, now.Add(24*time.Hour), time.UTC, accessReservation); err != actions.ErrExpired {
		t.Fatal("expired retry accepted", err)
	}
	if _, err := s.ReconcileNow(ctx, "alina", job.Attempt.ID, now); err != actions.ErrConflict {
		t.Fatal("terminal manual check", err)
	}
	if _, err := s.ReconcileNow(ctx, "alina", "bad", now); err != actions.ErrMissing {
		t.Fatal(err)
	}
}
func TestRefreshProjectionUsesSameVersionRules(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	result := objectResult("ACC-9004", "approved", 3, now)
	if err := s.ApplyObject(ctx, "alina", *result.Object, now); err != nil {
		t.Fatal(err)
	}
	old := objectResult("ACC-9004", "created", 1, now)
	if err := s.ApplyObject(ctx, "alina", *old.Object, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyObject(ctx, "maxim", *result.Object, now); err != actions.ErrContract {
		t.Fatal("foreign refresh", err)
	}
	var version int64
	if err := s.Pool.QueryRow(ctx, `SELECT service_version FROM service_objects WHERE object_id='ACC-9004'`).Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
}

func TestMetricsUseLatestAttemptAndExcludeRawData(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	s.RecordCall(ctx, "alina", "fake:recognize", "ok", 25)
	s.RecordCall(ctx, "alina", "fake:recognize", "timeout", 40)
	s.RecordCall(ctx, "alina", "raw-private-code", "raw-secret", 1)
	s.RecordLocal(ctx, "alina", "plan", 7)
	var raw []byte
	if e := s.Pool.QueryRow(ctx, metrics.ReportSQL).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var report map[string]any
	if json.Unmarshal(raw, &report) != nil || report["mode"] != "technical_proxies_only" || strings.Contains(string(raw), "raw-secret") {
		t.Fatal(string(raw))
	}
	var count int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE code='llm_call' AND scenario='raw-private-code'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if e := s.ExpireDrafts(ctx, now.Add(10*365*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
}

func TestExpiredUnavailableCanBeClosedBeforeFreshConfirmation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.OperationalClock = clock.Fixed{Instant: time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)}
	reservationKey := "expiry:" + actions.ID()
	validate := func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
		return actions.Reservation{Key: reservationKey}, nil
	}
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	card, e := s.CreateCard(ctx, "alina", "access_create", json.RawMessage(`{"resource_id":"monitoring"}`), now, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	attempt, e := s.ConfirmCard(ctx, "alina", card.ID, 1, now, time.UTC, validate)
	if e != nil {
		t.Fatal(e)
	}
	work, e := s.Claim(ctx, s.OperationalClock.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, work, actions.Result{Kind: actions.Unavailable, Code: "SERVICE_UNAVAILABLE", Source: "service"}, now); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Retry(ctx, "alina", attempt.ID, now.Add(time.Hour), time.UTC, validate); e != actions.ErrExpired {
		t.Fatal(e)
	}
	if _, e = s.ReconcileNow(ctx, "alina", attempt.ID, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	work, e = s.Claim(ctx, s.OperationalClock.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.NotCreated(ctx, work, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.CreateCard(ctx, "alina", "access_create", card.Parameters, now.Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM attempts WHERE card_id=$1`, fresh.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("fresh draft executed without confirmation", count, e)
	}
	confirmed, e := s.ConfirmCard(ctx, "alina", fresh.ID, 1, now.Add(time.Hour), time.UTC, validate)
	if e != nil || confirmed.OperationKey == attempt.OperationKey {
		t.Fatal(confirmed, e)
	}
	// Leave the shared test fixture without an active reservation.
	work, e = s.Claim(ctx, s.OperationalClock.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, work, actions.Result{Kind: actions.Rejected, Code: "NOT_ALLOWED", Source: "local_validation"}, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
}

func TestMetricsDeduplicateSourceAndSavedAction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	work, now := jobFixture(t, s)
	ids := []int64{}
	for i := 0; i < 3; i++ {
		m, _, err := s.AcceptMessage(ctx, "alina", actions.ID(), "text", "", safety.Prepare(ctx, safety.Local{}, "Демонстрационный вопрос"), now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Limit this transaction's report to a controlled cohort; rollback preserves history.
	if _, err = tx.Exec(ctx, `DELETE FROM jobs WHERE kind='message' AND NOT(message_id=ANY($1::bigint[]))`, ids); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE cards SET origin_message_id=$2 WHERE id=$1;`, work.Attempt.CardID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state='succeeded' WHERE id=$1`, work.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		result := `{"items":[{"answer":{"sources":[{"id":"demo"}]}}]}`
		if i == 1 {
			result = `{"items":[{"answer":{"sources":[{"id":"demo"}]}},{"contact":{"contact":{"id":"igor"}}}]}`
		}
		if i == 2 {
			result = `{"items":[]}`
		}
		if _, err = tx.Exec(ctx, `UPDATE jobs SET state='done',result=$2,payload='{"intent":"find_doc"}' WHERE message_id=$1`, id, result); err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err = tx.QueryRow(ctx, metrics.ReportSQL).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Resolution struct {
			Resolved  float64 `json:"resolved_messages"`
			Questions float64 `json:"organizational_messages"`
			Ratio     float64 `json:"ratio"`
		} `json:"U-2"`
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Resolution.Resolved != 1 || report.Resolution.Questions != 3 || report.Resolution.Ratio != 1.0/3 {
		t.Fatal(string(raw))
	}
}

func TestObjectObservationUsesOperationalClockAndIsStable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	business := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	operational := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	s.OperationalClock = clock.Fixed{Instant: operational}
	object := objectResult("ACC-990019", "approved", 1, business).Object
	if err := s.ApplyObject(ctx, "alina", *object, business); err != nil {
		t.Fatal(err)
	}
	s.OperationalClock = clock.Fixed{Instant: operational.Add(time.Hour)}
	if err := s.ApplyObject(ctx, "alina", *object, business); err != nil {
		t.Fatal(err)
	}
	var observed time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT observed_at FROM service_objects WHERE object_id=$1`, object.ID).Scan(&observed); err != nil || !observed.Equal(operational) {
		t.Fatal(observed, err)
	}
}

func assertAttemptRoute(t *testing.T, s *Store, attempt, state, result string) {
	t.Helper()
	ids, e := s.ExecutionIDs(context.Background(), "alina", "attempt", attempt)
	if e != nil || len(ids) != 1 {
		t.Fatal(ids, e)
	}
	page, e := (ExecutionRepository{Store: s}).Read(context.Background(), "alina", ids[0], 0, 100)
	if e != nil || page.Snapshot.State != state || page.Snapshot.ResultCode != result {
		t.Fatal(page, e)
	}
	if state == "manual_review" && execution.Active(page.Snapshot.State) {
		t.Fatal("manual review kept polling")
	}
}
