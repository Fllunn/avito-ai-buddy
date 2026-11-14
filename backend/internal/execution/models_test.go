package execution

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestJournal(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	m := NewMemory(func() time.Time { return now })
	ctx := context.Background()
	r := Run{ID: "run", UserID: "alina", State: "queued", StartedAt: now, UpdatedAt: now, DetailComplete: true}
	if e := m.Create(ctx, r); e != nil {
		t.Fatal(e)
	}
	if m.Create(ctx, r) != ErrConflict {
		t.Fatal("duplicate run")
	}
	fact := Fact{Key: "context:1", Node: Node{ID: "context", Kind: "context", LabelCode: "context", State: "succeeded", StartedAt: &now, FinishedAt: &now}, State: "running", At: now}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := m.Append(ctx, "run", fact); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	fact.Key = "answer:1"
	fact.Node = Node{ID: "answer", Kind: "answer", LabelCode: "answer", State: "succeeded"}
	fact.State = "completed"
	if e := m.Append(ctx, "run", fact); e != nil {
		t.Fatal(e)
	}
	p, e := m.Read(ctx, "alina", "run", 0, 1)
	if e != nil || len(p.Events) != 1 || p.NextSequence != 1 || p.Snapshot.LastSequence != 2 || !p.HasMore || len(p.Snapshot.Nodes) != 2 {
		t.Fatal(p, e)
	}
	p, e = m.Read(ctx, "alina", "run", p.NextSequence, 1)
	if e != nil || p.HasMore || p.NextSequence != 2 || p.Snapshot.State != "completed" {
		t.Fatal(p, e)
	}
	p.Snapshot.Nodes[0].State = "failed"
	p, _ = m.Read(ctx, "alina", "run", 2, 1)
	if p.Snapshot.Nodes[0].State != "succeeded" {
		t.Fatal("mutated snapshot")
	}
	if _, e = m.Read(ctx, "maxim", "run", 0, 1); e != ErrMissing {
		t.Fatal(e)
	}
	if _, e = m.Read(ctx, "alina", "run", -1, 1); e != ErrInvalid {
		t.Fatal(e)
	}
	if _, e = m.Read(ctx, "alina", "missing", 0, 1); e != ErrMissing {
		t.Fatal(e)
	}
	fact.At = now.Add(-time.Second)
	fact.Key = "late"
	if m.Append(ctx, "run", fact) != ErrConflict {
		t.Fatal("late fact")
	}
	fact.Node.LabelCode = "secret"
	if m.Append(ctx, "run", fact) != ErrInvalid {
		t.Fatal("unsafe label")
	}
	fact.Node.LabelCode = "answer"
	fact.At = now
	if m.Append(ctx, "missing", fact) != ErrMissing {
		t.Fatal("missing route")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if m.Create(cancelled, r) == nil || m.Append(cancelled, "run", fact) == nil {
		t.Fatal("ignored cancellation")
	}
	if _, e = m.Read(cancelled, "alina", "run", 0, 1); e == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestAllowlist(t *testing.T) {
	now := time.Now()
	base := Fact{Key: "x", Node: Node{ID: "service", Kind: "service", LabelCode: "service", State: "waiting_service"}, State: "running", At: now}
	if !ValidFact(base) {
		t.Fatal("valid fact")
	}
	for _, mutate := range []func(*Fact){func(f *Fact) { f.Key = "" }, func(f *Fact) { f.State = "invented" }, func(f *Fact) { f.Node.Kind = "salary" }, func(f *Fact) { f.Node.State = "approved" }, func(f *Fact) { f.Node.DetailCode = "raw payload" }, func(f *Fact) { f.Node.ServiceCode = "secret" }, func(f *Fact) { f.ResultCode = "token" }, func(f *Fact) { future := now.Add(time.Second); f.Node.StartedAt = &future }, func(f *Fact) { f.Node.FinishedAt = &now }} {
		f := base
		mutate(&f)
		if ValidFact(f) {
			t.Fatal("unsafe fact", f)
		}
	}
	for _, s := range []string{"queued", "running", "waiting_user", "reconciling", "manual_review", "completed", "rejected", "failed", "cancelled"} {
		if !ValidState(s) {
			t.Fatal(s)
		}
	}
	if Active("waiting_user") || !Active("reconciling") {
		t.Fatal("polling")
	}
}
