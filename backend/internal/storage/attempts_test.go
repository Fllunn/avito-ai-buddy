package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func accessReservation(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
	return actions.Reservation{Key: "resource:monitoring"}, nil
}
func TestConcurrentConfirmationAndImmutableAttempt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	card, err := s.CreateCard(ctx, "alina", "access_create", json.RawMessage(`{"resource_id":"monitoring"}`), now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan actions.Attempt, 2)
	for range 2 {
		wg.Go(func() {
			a, err := s.ConfirmCard(ctx, "alina", card.ID, 1, now, time.UTC, accessReservation)
			if err != nil {
				t.Error(err)
			}
			results <- a
		})
	}
	wg.Wait()
	close(results)
	var first actions.Attempt
	for a := range results {
		if first.ID != "" && first.ID != a.ID {
			t.Fatal("duplicate attempt")
		}
		first = a
	}
	if first.ID == "" || first.JobID == "" || first.OperationKey == "" || first.State != "queued" {
		t.Fatal(first)
	}
	persisted, err := s.Attempt(ctx, "alina", first.ID)
	if err != nil || persisted.OperationKey != first.OperationKey {
		t.Fatal(persisted, err)
	}
	if _, err := s.Attempt(ctx, "maxim", first.ID); err != actions.ErrMissing {
		t.Fatal("foreign attempt", err)
	}
	if _, err := s.Attempt(ctx, "alina", "bad"); err != actions.ErrMissing {
		t.Fatal(err)
	}
	if _, err := s.EditCard(ctx, "alina", card.ID, 1, json.RawMessage(`{}`), now, time.Hour); err != actions.ErrConflict {
		t.Fatal("snapshot editable", err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO attempts(id,user_id,card_id,card_version,number,operation_key,parameters,state,conflict_key,created_at,updated_at) SELECT $1,user_id,card_id,card_version,2,operation_key,parameters,'queued','other',created_at,updated_at FROM attempts WHERE id=$2`, actions.ID(), first.ID); err == nil {
		t.Fatal("operation key uniqueness absent")
	}
}
func TestConfirmRechecksRightsVersionsAndTTL(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	create := func(user string) actions.Card {
		t.Helper()
		c, err := s.CreateCard(ctx, user, "access_create", json.RawMessage(`{}`), now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	card := create("maxim")
	if _, err := s.ConfirmCard(ctx, "alina", card.ID, 1, now, time.UTC, accessReservation); err != actions.ErrMissing {
		t.Fatal(err)
	}
	if _, err := s.ConfirmCard(ctx, "maxim", card.ID, 2, now, time.UTC, accessReservation); err != actions.ErrConflict {
		t.Fatal(err)
	}
	if _, err := s.ConfirmCard(ctx, "maxim", card.ID, 1, now.Add(time.Hour), time.UTC, accessReservation); err != actions.ErrExpired {
		t.Fatal(err)
	}
	if _, err := s.ConfirmCard(ctx, "maxim", card.ID, 1, now, time.UTC, nil); err != actions.ErrInvalid {
		t.Fatal(err)
	}
	denied := func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
		return actions.Reservation{}, authz.ErrNotAllowed
	}
	if _, err := s.ConfirmCard(ctx, "maxim", card.ID, 1, now, time.UTC, denied); err != authz.ErrNotAllowed {
		t.Fatal(err)
	}
	revoke := func(ctx context.Context, p demo.Profile, _ actions.Card) (actions.Reservation, error) {
		_, err := s.Pool.Exec(ctx, `UPDATE users SET policy_version=policy_version+1 WHERE id=$1`, p.ID)
		return actions.Reservation{Key: "revocation"}, err
	}
	if _, err := s.ConfirmCard(ctx, "maxim", card.ID, 1, now, time.UTC, revoke); err != authz.ErrNotAllowed {
		t.Fatal("revoked during preflight", err)
	}
	buddy := create("igor")
	if _, err := s.ConfirmCard(ctx, "igor", buddy.ID, 1, now, time.UTC, accessReservation); err != authz.ErrNotAllowed {
		t.Fatal("buddy action", err)
	}
}
func TestConflictingCardsAndAtomicJobFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	// Different card types can still reserve the same service resource.
	cards := []actions.Card{}
	for _, kind := range []string{"access_create", "access_cancel"} {
		c, err := s.CreateCard(ctx, "maxim", kind, json.RawMessage(`{}`), now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		cards = append(cards, c)
	}
	unique := func(context.Context, demo.Profile, actions.Card) (actions.Reservation, error) {
		return actions.Reservation{Key: "resource:test-conflict"}, nil
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, c := range cards {
		wg.Go(func() { _, err := s.ConfirmCard(ctx, "maxim", c.ID, 1, now, time.UTC, unique); outcomes <- err })
	}
	wg.Wait()
	close(outcomes)
	successes, conflicts := 0, 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if errors.Is(err, actions.ErrActive) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	// A failed job insertion must roll back the attempt and confirmation together.
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_test_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER fail_test_job BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION fail_test_job()`); err != nil {
		t.Fatal(err)
	}
	defer s.Pool.Exec(ctx, `DROP TRIGGER fail_test_job ON jobs; DROP FUNCTION fail_test_job()`)
	c, err := s.CreateCard(ctx, "alina", "access_cancel", json.RawMessage(`{}`), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmCard(ctx, "alina", c.ID, 1, now, time.UTC, unique); err == nil {
		t.Fatal("job failure ignored")
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM attempts WHERE card_id=$1`, c.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial attempt", count, err)
	}
	current, err := s.Card(ctx, "alina", c.ID, now)
	if err != nil || current.State != "draft" {
		t.Fatal(current, err)
	}
}
