package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMessagesAreDurableAndDeduplicated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	client := actions.ID()
	text := safety.Prepare(ctx, safety.Local{}, "Хочу отгул: диагноз диабет")
	var wg sync.WaitGroup
	results := make(chan actions.Message, 2)
	for range 2 {
		wg.Go(func() {
			m, _, err := s.AcceptMessage(ctx, "alina", client, "text", "", text, now)
			if err != nil {
				t.Error(err)
			}
			results <- m
		})
	}
	wg.Wait()
	close(results)
	var first int64
	for m := range results {
		if first != 0 && first != m.ID {
			t.Fatal("duplicate message")
		}
		first = m.ID
	}
	if _, _, err := s.AcceptMessage(ctx, "alina", client, "text", "", safety.Prepare(ctx, safety.Local{}, "другое очищенное содержимое"), now); !errors.Is(err, actions.ErrMessageConflict) {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptMessage(ctx, "alina", client, "button", "next_step", text, now); !errors.Is(err, actions.ErrMessageConflict) {
		t.Fatal(err)
	}
	messages, err := s.Messages(ctx, "alina", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.ID == first && (m.Text != safety.Hidden || !m.FormRequired) {
			t.Fatal("sensitive content persisted")
		}
	}
	foreign, err := s.Messages(ctx, "maxim", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range foreign {
		if m.ID == first {
			t.Fatal("foreign history")
		}
	}
	var jobs int
	var owner string
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),min(user_id) FROM jobs WHERE message_id=$1`, first).Scan(&jobs, &owner); err != nil || jobs != 1 || owner != "alina" {
		t.Fatal(jobs, owner, err)
	}
	restarted, err := Open(ctx, s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	again, created, err := restarted.AcceptMessage(ctx, "alina", client, "text", "", text, now)
	if err != nil || created || again.ID != first {
		t.Fatal(again, created, err)
	}
	for _, tc := range []struct{ id, kind, intent string }{{"bad", "text", ""}, {actions.ID(), "unknown", ""}, {actions.ID(), "text", "next_step"}, {actions.ID(), "button", "unknown"}} {
		if _, _, err := s.AcceptMessage(ctx, "alina", tc.id, tc.kind, tc.intent, text, now); !errors.Is(err, actions.ErrInvalid) {
			t.Fatal(tc, err)
		}
	}
}
func TestCardVersionsReplacementAndExpiration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	card, err := s.CreateCard(ctx, "alina", "access_create", json.RawMessage(`{"resource_id":"tracker-pay"}`), now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if foreign, err := s.Card(ctx, "maxim", card.ID, now); err != actions.ErrMissing || foreign.ID != "" {
		t.Fatal(foreign, err)
	}
	if _, err := s.Card(ctx, "alina", "bad", now); err != actions.ErrMissing {
		t.Fatal(err)
	}
	changed, err := s.EditCard(ctx, "alina", card.ID, 1, json.RawMessage(`{"resource_id":"monitoring"}`), now.Add(time.Hour), ttl)
	if err != nil || changed.Version != 2 || !changed.ExpiresAt.Equal(now.Add(25*time.Hour)) {
		t.Fatal(changed, err)
	}
	if _, err := s.EditCard(ctx, "alina", card.ID, 1, changed.Parameters, now, ttl); err != actions.ErrConflict {
		t.Fatal("stale edit", err)
	}
	expired, err := s.Card(ctx, "alina", card.ID, now.Add(25*time.Hour))
	if err != nil || expired.State != "expired" {
		t.Fatal(expired, err)
	}
	if _, err := s.EditCard(ctx, "alina", card.ID, 2, changed.Parameters, now.Add(25*time.Hour), ttl); err != actions.ErrExpired {
		t.Fatal(err)
	}
	replacement, err := s.CreateCard(ctx, "alina", "access_create", card.Parameters, now.Add(26*time.Hour), ttl)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Card(ctx, "alina", card.ID, now.Add(26*time.Hour))
	if err != nil || old.State != "expired" {
		t.Fatal(old, err)
	}
	next, err := s.CreateCard(ctx, "alina", "access_create", card.Parameters, now.Add(27*time.Hour), ttl)
	if err != nil {
		t.Fatal(err)
	}
	old, err = s.Card(ctx, "alina", replacement.ID, now.Add(27*time.Hour))
	if err != nil || old.State != "replaced" {
		t.Fatal(old, err)
	}
	cancelled, err := s.CancelCard(ctx, "alina", next.ID, 1, now.Add(28*time.Hour))
	if err != nil || cancelled.State != "discarded" {
		t.Fatal(cancelled, err)
	}
	if _, err := s.CancelCard(ctx, "alina", next.ID, 1, now.Add(28*time.Hour)); err != nil {
		t.Fatal("duplicate cancellation", err)
	}
	var attempts int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM attempts WHERE card_id=$1`, next.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("cancel draft triggered attempt", err)
	}
	var versions int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM card_versions WHERE card_id=$1`, card.ID).Scan(&versions); err != nil || versions != 2 {
		t.Fatal(versions, err)
	}
}
func TestConcurrentCardEdits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	card, err := s.CreateCard(ctx, "maxim", "time_off_create", json.RawMessage(`{"start_date":"2026-11-13"}`), now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := s.EditCard(ctx, "maxim", card.ID, 1, json.RawMessage(`{"start_date":"2026-11-16"}`), now.Add(time.Hour), 24*time.Hour)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, actions.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}
func TestCardValidationAndStorageFailures(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2027, 1, 11, 7, 0, 0, 0, time.UTC)
	valid := json.RawMessage(`{}`)
	for _, tc := range []struct {
		kind    string
		payload json.RawMessage
		ttl     time.Duration
	}{{"bad", valid, time.Hour}, {"access_create", json.RawMessage(`bad`), time.Hour}, {"access_create", valid, 0}, {"access_create", json.RawMessage(`"` + strings.Repeat("x", 8192) + `"`), time.Hour}} {
		if _, err := s.CreateCard(ctx, "alina", tc.kind, tc.payload, now, tc.ttl); err != actions.ErrInvalid {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateCard(ctx, "missing", "access_create", valid, now, time.Hour); err != actions.ErrMissing {
		t.Fatal(err)
	}
	if _, err := s.CancelCard(ctx, "alina", "bad", 1, now); err != actions.ErrMissing {
		t.Fatal(err)
	}
	card, err := s.CreateCard(ctx, "alina", "access_create", valid, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditCard(ctx, "alina", card.ID, 1, json.RawMessage(`bad`), now, time.Hour); err != actions.ErrInvalid {
		t.Fatal(err)
	}
	if _, err := s.CancelCard(ctx, "alina", actions.ID(), 1, now); err != actions.ErrMissing {
		t.Fatal(err)
	}
	s.Close()
	if _, err := s.CreateCard(ctx, "alina", "access_create", valid, now, time.Hour); err == nil {
		t.Fatal("closed store")
	}
	if _, err := s.CancelCard(ctx, "alina", card.ID, 1, now); err == nil {
		t.Fatal("closed store")
	}
	if _, _, err := s.AcceptMessage(ctx, "alina", actions.ID(), "text", "", safety.Prepare(ctx, safety.Local{}, "test"), now); err == nil {
		t.Fatal("closed store")
	}
	if _, err := s.Messages(ctx, "alina", 0); err == nil {
		t.Fatal("closed store")
	}
}

func TestButtonSlotsShareMessageIdentity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	client := actions.ID()
	slots := actions.Slots{ResourceID: "tracker-pay", Level: "edit", Justification: safety.Prepare(ctx, safety.Local{}, "Для задач стажировки")}
	first, created, err := s.AcceptAction(ctx, "alina", client, "access_request", slots, now)
	if err != nil || !created {
		t.Fatal(first, created, err)
	}
	same, created, err := s.AcceptAction(ctx, "alina", client, "access_request", slots, now)
	if err != nil || created || first.ID != same.ID {
		t.Fatal(same, created, err)
	}
	slots.Level = "read"
	if _, _, err := s.AcceptAction(ctx, "alina", client, "access_request", slots, now); err != actions.ErrMessageConflict {
		t.Fatal("slots conflict missed", err)
	}
	slots.ResourceID = "private medical text"
	if _, _, err := s.AcceptAction(ctx, "alina", actions.ID(), "access_request", slots, now); err != actions.ErrInvalid {
		t.Fatal("unvalidated slot stored", err)
	}
}

func TestConcurrentDraftsLeaveOneActiveCard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := s.CreateCard(ctx, "alina", "time_off_create", json.RawMessage(`{"start_date":"2026-11-13","end_date":"2026-11-13"}`), now, 24*time.Hour); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var active int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM cards WHERE user_id='alina' AND type='time_off_create' AND state='draft'`).Scan(&active); err != nil || active != 1 {
		t.Fatal(active, err)
	}
}
