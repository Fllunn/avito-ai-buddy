package execution

import (
	"context"
	"errors"
	"time"
)

var ErrInvalid = errors.New("VALIDATION_ERROR")
var ErrMissing = errors.New("NOT_FOUND")
var ErrConflict = errors.New("EXECUTION_CONFLICT")

type Node struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	LabelCode   string     `json:"label_code"`
	DetailCode  string     `json:"detail_code,omitempty"`
	ServiceCode string     `json:"service_code,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}
type Link struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version int64  `json:"version,omitempty"`
}
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type Event struct {
	Sequence int64     `json:"sequence"`
	Node     Node      `json:"node"`
	State    string    `json:"state"`
	At       time.Time `json:"at"`
}
type Run struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	Scenario       string    `json:"scenario"`
	State          string    `json:"state"`
	CurrentNodeID  string    `json:"current_node_id"`
	Revision       int64     `json:"revision"`
	LastSequence   int64     `json:"last_sequence"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ResultCode     string    `json:"result_code,omitempty"`
	DetailComplete bool      `json:"detail_complete"`
	Nodes          []Node    `json:"nodes"`
	Edges          []Edge    `json:"edges"`
	Children       []Run     `json:"children,omitempty"`
	Links          []Link    `json:"links"`
}
type Page struct {
	Snapshot       Run       `json:"snapshot"`
	Events         []Event   `json:"events"`
	NextSequence   int64     `json:"next_sequence"`
	HasMore        bool      `json:"has_more"`
	AsOf           time.Time `json:"as_of"`
	DetailComplete bool      `json:"detail_complete"`
}
type List struct {
	Items      []Run  `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type Fact struct {
	Key        string
	Node       Node
	State      string
	ResultCode string
	At         time.Time
}
type Repository interface {
	Create(context.Context, Run) error
	Append(context.Context, string, Fact) error
	Read(context.Context, string, string, int64, int) (Page, error)
}

func Active(state string) bool {
	return state == "queued" || state == "running" || state == "reconciling"
}
func ValidState(s string) bool {
	switch s {
	case "queued", "running", "waiting_user", "reconciling", "manual_review", "completed", "rejected", "failed", "cancelled":
		return true
	}
	return false
}
func validCode(s string) bool {
	switch s {
	case "context", "model", "sources", "validation", "clarification", "confirmation", "queue", "service", "persistence", "reconciliation", "answer":
		return true
	}
	return false
}
func ValidFact(f Fact) bool {
	if f.Key == "" || len(f.Key) > 200 || !ValidState(f.State) || f.At.IsZero() || !validCode(f.Node.Kind) || f.Node.ID != f.Node.Kind || f.Node.LabelCode != f.Node.Kind {
		return false
	}
	switch f.Node.State {
	case "pending", "running", "waiting_user", "waiting_service", "succeeded", "skipped", "rejected", "failed", "unknown":
	default:
		return false
	}
	switch f.Node.DetailCode {
	case "", "local", "remote", "fallback", "manual_review", "not_created", "cancelled", "expired", "replaced", "partial", "created", "submitted", "approved":
	default:
		return false
	}
	switch f.Node.ServiceCode {
	case "", "knowledge", "access", "time_off":
	default:
		return false
	}
	switch f.ResultCode {
	case "", "answer", "created", "submitted", "cancelled", "rejected", "unavailable", "unknown", "manual_review", "not_created", "partial", "expired", "replaced", "fallback":
	default:
		return false
	}
	if f.Node.StartedAt != nil && f.Node.StartedAt.After(f.At) || f.Node.FinishedAt != nil && (f.Node.FinishedAt.After(f.At) || f.Node.StartedAt == nil || f.Node.FinishedAt.Before(*f.Node.StartedAt)) {
		return false
	}
	return true
}
func Apply(r *Run, f Fact) Event {
	r.LastSequence++
	r.Revision++
	r.State = f.State
	r.CurrentNodeID = f.Node.ID
	r.UpdatedAt = f.At
	r.ResultCode = f.ResultCode
	found := false
	for i, n := range r.Nodes {
		if n.ID == f.Node.ID {
			r.Nodes[i] = f.Node
			found = true
			break
		}
	}
	if !found {
		if len(r.Nodes) > 0 {
			r.Edges = append(r.Edges, Edge{r.Nodes[len(r.Nodes)-1].ID, f.Node.ID})
		}
		r.Nodes = append(r.Nodes, f.Node)
	}
	return Event{r.LastSequence, f.Node, f.State, f.At}
}
