package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/execution"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ExecutionRepository struct{ Store *Store }

var _ execution.Repository = ExecutionRepository{}

func (r ExecutionRepository) Create(ctx context.Context, run execution.Run) error {
	tx, e := r.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = createExecution(ctx, tx, run); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func createExecution(ctx context.Context, tx pgx.Tx, r execution.Run) error {
	if !actions.ValidID(r.ID) || r.UserID == "" || !execution.ValidState(r.State) || r.StartedAt.IsZero() {
		return execution.ErrInvalid
	}
	_, e := tx.Exec(ctx, `INSERT INTO execution_runs(id,user_id,scenario,state,started_at,updated_at,detail_complete) VALUES($1,$2,$3,$4,$5,$5,$6)`, r.ID, r.UserID, r.Scenario, r.State, r.StartedAt, r.DetailComplete)
	if e != nil {
		return e
	}
	for _, l := range r.Links {
		if e = linkExecution(ctx, tx, r.ID, r.UserID, l); e != nil {
			return e
		}
	}
	return nil
}
func linkExecution(ctx context.Context, tx pgx.Tx, id, user string, l execution.Link) error {
	var owned bool
	switch l.Kind {
	case "message":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id::text=$1 AND user_id=$2)`, l.ID, user).Scan(&owned)
		if e != nil {
			return e
		}
	case "job":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id::text=$1 AND user_id=$2)`, l.ID, user).Scan(&owned)
		if e != nil {
			return e
		}
	case "card":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cards c JOIN card_versions v ON v.card_id=c.id WHERE c.id::text=$1 AND c.user_id=$2 AND v.version=$3)`, l.ID, user, l.Version).Scan(&owned)
		if e != nil {
			return e
		}
	case "attempt":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE id::text=$1 AND user_id=$2)`, l.ID, user).Scan(&owned)
		if e != nil {
			return e
		}
	case "parent", "operation_parent":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_runs WHERE id::text=$1 AND user_id=$2)`, l.ID, user).Scan(&owned)
		if e != nil {
			return e
		}
	case "object":
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_objects WHERE service||':'||object_id=$1 AND user_id=$2)`, l.ID, user).Scan(&owned)
		if e != nil {
			return e
		}
	default:
		return execution.ErrInvalid
	}
	if !owned {
		return execution.ErrMissing
	}
	_, e := tx.Exec(ctx, `INSERT INTO execution_links(run_id,user_id,kind,object_id,version) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, user, l.Kind, l.ID, l.Version)
	return e
}
func (r ExecutionRepository) Append(ctx context.Context, id string, f execution.Fact) error {
	tx, e := r.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = appendExecution(ctx, tx, id, f); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func appendExecution(ctx context.Context, tx pgx.Tx, id string, f execution.Fact) error {
	if !execution.ValidFact(f) || !actions.ValidID(id) {
		return execution.ErrInvalid
	}
	var sequence int64
	var updated time.Time
	if e := tx.QueryRow(ctx, `SELECT last_sequence,updated_at FROM execution_runs WHERE id=$1 FOR UPDATE`, id).Scan(&sequence, &updated); e != nil {
		if e == pgx.ErrNoRows {
			return execution.ErrMissing
		}
		return e
	}
	var exists bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_events WHERE run_id=$1 AND fact_key=$2)`, id, f.Key).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	if f.At.Before(updated) {
		return execution.ErrConflict
	}
	// Preserve the observed start of a stage through its completion.
	if f.Node.State != "pending" {
		var started *time.Time
		e := tx.QueryRow(ctx, `SELECT started_at FROM execution_events WHERE run_id=$1 AND node_id=$2 AND started_at IS NOT NULL ORDER BY sequence LIMIT 1`, id, f.Node.ID).Scan(&started)
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		if started != nil {
			f.Node.StartedAt = started
		}
	}
	sequence++
	_, e := tx.Exec(ctx, `INSERT INTO execution_events(run_id,sequence,fact_key,node_id,kind,node_state,run_state,detail_code,service_code,result_code,started_at,finished_at,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, sequence, f.Key, f.Node.ID, f.Node.Kind, f.Node.State, f.State, f.Node.DetailCode, f.Node.ServiceCode, f.ResultCode, f.Node.StartedAt, f.Node.FinishedAt, f.At)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE execution_runs SET last_sequence=$2,revision=revision+1,state=$3,current_node_id=$4,result_code=$5,updated_at=$6 WHERE id=$1`, id, sequence, f.State, f.Node.ID, f.ResultCode, f.At)
	if e != nil {
		return e
	}
	return refreshTransfer(ctx, tx, id, f)
}

const runColumns = `r.id::text,r.user_id,r.scenario,r.state,r.current_node_id,r.revision,r.last_sequence,r.started_at,r.updated_at,r.result_code,r.detail_complete`

func scanRun(row pgx.Row) (execution.Run, error) {
	var r execution.Run
	e := row.Scan(&r.ID, &r.UserID, &r.Scenario, &r.State, &r.CurrentNodeID, &r.Revision, &r.LastSequence, &r.StartedAt, &r.UpdatedAt, &r.ResultCode, &r.DetailComplete)
	r.Nodes = []execution.Node{}
	r.Edges = []execution.Edge{}
	r.Links = []execution.Link{}
	return r, e
}

// Only generic allowlisted labels are projected. Object titles and payloads never enter this DTO.
func (r ExecutionRepository) Read(ctx context.Context, user, id string, after int64, limit int) (execution.Page, error) {
	if !actions.ValidID(id) {
		return execution.Page{}, execution.ErrMissing
	}
	if after < 0 || limit < 1 || limit > 100 {
		return execution.Page{}, execution.ErrInvalid
	}
	tx, e := r.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return execution.Page{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	run, e := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM execution_runs r JOIN users u ON u.id=r.user_id AND u.enabled WHERE r.id=$1 AND r.user_id=$2`, id, user))
	if e == pgx.ErrNoRows {
		return execution.Page{}, execution.ErrMissing
	}
	if e != nil {
		return execution.Page{}, e
	}
	rows, e := tx.Query(ctx, `SELECT sequence,node_id,kind,node_state,run_state,detail_code,service_code,started_at,finished_at,observed_at FROM execution_events WHERE run_id=$1 ORDER BY sequence`, id)
	if e != nil {
		return execution.Page{}, e
	}
	p := execution.Page{Snapshot: run, Events: []execution.Event{}, NextSequence: after, AsOf: r.Store.OperationalClock.Now(), DetailComplete: run.DetailComplete}
	for rows.Next() {
		var ev execution.Event
		if e = rows.Scan(&ev.Sequence, &ev.Node.ID, &ev.Node.Kind, &ev.Node.State, &ev.State, &ev.Node.DetailCode, &ev.Node.ServiceCode, &ev.Node.StartedAt, &ev.Node.FinishedAt, &ev.At); e != nil {
			rows.Close()
			return p, e
		}
		ev.Node.LabelCode = ev.Node.Kind
		found := false
		for i, n := range p.Snapshot.Nodes {
			if n.ID == ev.Node.ID {
				p.Snapshot.Nodes[i] = ev.Node
				found = true
				break
			}
		}
		if !found {
			if len(p.Snapshot.Nodes) > 0 {
				p.Snapshot.Edges = append(p.Snapshot.Edges, execution.Edge{From: p.Snapshot.Nodes[len(p.Snapshot.Nodes)-1].ID, To: ev.Node.ID})
			}
			p.Snapshot.Nodes = append(p.Snapshot.Nodes, ev.Node)
		}
		if ev.Sequence > after {
			if len(p.Events) < limit {
				p.Events = append(p.Events, ev)
				p.NextSequence = ev.Sequence
			} else {
				p.HasMore = true
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	// Links are intentionally limited to owned route relationships. Raw source/object ids are not exposed.
	rows, e = tx.Query(ctx, `SELECT kind,object_id,version FROM execution_links WHERE run_id=$1 AND kind IN ('parent','operation_parent','job') ORDER BY kind,object_id,version`, id)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var l execution.Link
		if e = rows.Scan(&l.Kind, &l.ID, &l.Version); e != nil {
			rows.Close()
			return p, e
		}
		p.Snapshot.Links = append(p.Snapshot.Links, l)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	childRows, e := tx.Query(ctx, `SELECT r.id::text FROM execution_runs r JOIN execution_links l ON l.run_id=r.id WHERE l.kind='operation_parent' AND l.object_id=$1 AND r.user_id=$2 ORDER BY l.version,r.id`, id, user)
	if e != nil {
		return p, e
	}
	childIDs := []string{}
	for childRows.Next() {
		var childID string
		if e = childRows.Scan(&childID); e != nil {
			childRows.Close()
			return p, e
		}
		childIDs = append(childIDs, childID)
	}
	e = childRows.Err()
	childRows.Close()
	if e != nil {
		return p, e
	}
	for _, childID := range childIDs {
		child, e := executionSnapshot(ctx, tx, user, childID)
		if e != nil {
			return p, e
		}
		p.Snapshot.Children = append(p.Snapshot.Children, child)
	}
	return p, tx.Commit(ctx)
}

type executionCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func (s *Store) Executions(ctx context.Context, user, cursor string, limit int, active bool) (execution.List, error) {
	result := execution.List{Items: []execution.Run{}}
	if limit < 1 || limit > 100 {
		return result, execution.ErrInvalid
	}
	var c executionCursor
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(raw, &c) != nil || c.At.IsZero() || !actions.ValidID(c.ID) {
			return result, execution.ErrInvalid
		}
	}
	rows, e := s.Pool.Query(ctx, `SELECT `+runColumns+` FROM execution_runs r JOIN users u ON u.id=r.user_id AND u.enabled WHERE r.user_id=$1 AND (NOT $2 OR r.state IN ('queued','running','waiting_user','reconciling','manual_review')) AND ($3='' OR (r.started_at,r.id)<($4,$5::uuid)) ORDER BY r.started_at DESC,r.id DESC LIMIT $6`, user, active, cursor, nullableCursorTime(c.At), nullableCursorID(c.ID), limit+1)
	if e != nil {
		return result, e
	}
	defer rows.Close()
	for rows.Next() {
		run, e := scanRun(rows)
		if e != nil {
			return result, e
		}
		if len(result.Items) == limit {
			last := result.Items[len(result.Items)-1]
			raw, _ := json.Marshal(executionCursor{last.StartedAt, last.ID})
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		result.Items = append(result.Items, run)
	}
	return result, rows.Err()
}
func nullableCursorTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func nullableCursorID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func executionMissing(e error) bool { return errors.Is(e, execution.ErrMissing) }

func (s *Store) ExecutionIDs(ctx context.Context, user, kind, id string) ([]string, error) {
	rows, e := s.Pool.Query(ctx, `SELECT run_id::text FROM execution_links WHERE user_id=$1 AND kind=$2 AND object_id=$3 ORDER BY run_id`, user, kind, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var value string
		if e = rows.Scan(&value); e != nil {
			return nil, e
		}
		ids = append(ids, value)
	}
	return ids, rows.Err()
}

func executionSnapshot(ctx context.Context, tx pgx.Tx, user, id string) (execution.Run, error) {
	r, e := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM execution_runs r WHERE id=$1 AND user_id=$2`, id, user))
	if e != nil {
		return r, e
	}
	rows, e := tx.Query(ctx, `SELECT node_id,kind,node_state,detail_code,service_code,started_at,finished_at FROM (SELECT DISTINCT ON(node_id) node_id,kind,node_state,detail_code,service_code,started_at,finished_at,min(sequence) OVER(PARTITION BY node_id) position FROM execution_events WHERE run_id=$1 ORDER BY node_id,sequence DESC) nodes ORDER BY position`, id)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	for rows.Next() {
		var n execution.Node
		if e = rows.Scan(&n.ID, &n.Kind, &n.State, &n.DetailCode, &n.ServiceCode, &n.StartedAt, &n.FinishedAt); e != nil {
			return r, e
		}
		n.LabelCode = n.Kind
		r.Nodes = append(r.Nodes, n)
	}
	return r, rows.Err()
}
