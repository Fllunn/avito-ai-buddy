package execution

import (
	"context"
	"sync"
	"time"
)

// Memory is a deterministic repository for tests; it never executes a business operation.
type Memory struct {
	mu     sync.Mutex
	Runs   map[string]Run
	Events map[string][]Event
	Facts  map[string]map[string]bool
	Now    func() time.Time
}

func NewMemory(now func() time.Time) *Memory {
	return &Memory{Runs: map[string]Run{}, Events: map[string][]Event{}, Facts: map[string]map[string]bool{}, Now: now}
}
func (m *Memory) Create(ctx context.Context, r Run) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Runs[r.ID]; ok {
		return ErrConflict
	}
	m.Runs[r.ID] = r
	m.Facts[r.ID] = map[string]bool{}
	return nil
}
func (m *Memory) Append(ctx context.Context, id string, f Fact) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !ValidFact(f) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Runs[id]
	if !ok {
		return ErrMissing
	}
	if m.Facts[id][f.Key] {
		return nil
	}
	if f.At.Before(r.UpdatedAt) {
		return ErrConflict
	}
	m.Events[id] = append(m.Events[id], Apply(&r, f))
	m.Runs[id] = r
	m.Facts[id][f.Key] = true
	return nil
}
func (m *Memory) Read(ctx context.Context, user, id string, after int64, limit int) (Page, error) {
	if e := ctx.Err(); e != nil {
		return Page{}, e
	}
	if after < 0 || limit < 1 || limit > 100 {
		return Page{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Runs[id]
	if !ok || r.UserID != user {
		return Page{}, ErrMissing
	}
	// Copy via fresh slices: callers cannot mutate the stored snapshot.
	r.Nodes = append([]Node{}, r.Nodes...)
	r.Edges = append([]Edge{}, r.Edges...)
	r.Links = append([]Link{}, r.Links...)
	p := Page{Snapshot: r, Events: []Event{}, NextSequence: after, AsOf: m.Now(), DetailComplete: r.DetailComplete}
	for _, e := range m.Events[id] {
		if e.Sequence <= after {
			continue
		}
		if len(p.Events) == limit {
			p.HasMore = true
			break
		}
		p.Events = append(p.Events, e)
		p.NextSequence = e.Sequence
	}
	return p, nil
}
