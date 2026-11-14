package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/execution"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

func routeFor(ctx context.Context, tx pgx.Tx, user, kind, id string) (string, error) {
	var run string
	e := tx.QueryRow(ctx, `SELECT run_id::text FROM execution_links WHERE user_id=$1 AND kind=$2 AND object_id=$3 ORDER BY version DESC LIMIT 1`, user, kind, id).Scan(&run)
	if e == pgx.ErrNoRows {
		return "", nil
	}
	return run, e
}
func fact(key, node, nodeState, state, result string, now time.Time) execution.Fact {
	n := execution.Node{ID: node, Kind: node, LabelCode: node, State: nodeState}
	if nodeState != "pending" {
		n.StartedAt = &now
	}
	if nodeState == "succeeded" || nodeState == "failed" || nodeState == "rejected" || nodeState == "skipped" {
		n.FinishedAt = &now
	}
	return execution.Fact{Key: key, Node: n, State: state, ResultCode: result, At: now}
}
func (s *Store) executionForMessage(ctx context.Context, tx pgx.Tx, m actions.Message, job string) error {
	now := s.OperationalClock.Now()
	id := actions.ID()
	r := execution.Run{ID: id, UserID: m.UserID, Scenario: "orientation", State: "queued", StartedAt: now, DetailComplete: true, Links: []execution.Link{{Kind: "message", ID: strconv.FormatInt(m.ID, 10)}, {Kind: "job", ID: job, Version: 1}}}
	if e := createExecution(ctx, tx, r); e != nil {
		return e
	}
	return appendExecution(ctx, tx, id, fact("message:"+strconv.FormatInt(m.ID, 10)+":queued", "queue", "pending", "queued", "", now))
}
func (s *Store) executionForCard(ctx context.Context, tx pgx.Tx, c actions.Card, message int64) error {
	id := execution.RouteID(ctx)
	var e error
	if id == "" && message > 0 {
		id, e = routeFor(ctx, tx, c.UserID, "message", strconv.FormatInt(message, 10))
		if e != nil {
			return e
		}
	}
	now := s.OperationalClock.Now()
	if id == "" {
		id = actions.ID()
		scenario := "access"
		if c.Type == "time_off_create" || c.Type == "time_off_cancel" {
			scenario = "time_off"
		}
		if e = createExecution(ctx, tx, execution.Run{ID: id, UserID: c.UserID, Scenario: scenario, State: "waiting_user", StartedAt: now, DetailComplete: true}); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE execution_runs SET scenario=$2 WHERE id=$1`, id, map[string]string{"access_create": "access", "access_cancel": "access", "time_off_create": "time_off", "time_off_cancel": "time_off"}[c.Type]); e != nil {
		return e
	}
	if e = linkExecution(ctx, tx, id, c.UserID, execution.Link{Kind: "card", ID: c.ID, Version: int64(c.Version)}); e != nil {
		return e
	}
	return appendExecution(ctx, tx, id, fact("card:"+c.ID+":"+strconv.Itoa(c.Version)+":shown", "confirmation", "waiting_user", "waiting_user", "", now))
}
func (s *Store) cardExecutionFact(ctx context.Context, tx pgx.Tx, c actions.Card, outcome string) error {
	id, e := routeFor(ctx, tx, c.UserID, "card", c.ID)
	if e != nil || id == "" {
		return e
	}
	if e = linkExecution(ctx, tx, id, c.UserID, execution.Link{Kind: "card", ID: c.ID, Version: int64(c.Version)}); e != nil {
		return e
	}
	state, nodeState, result := "waiting_user", "waiting_user", ""
	if outcome == "discarded" || outcome == "expired" || outcome == "replaced" {
		state, nodeState, result = "cancelled", "skipped", map[string]string{"discarded": "cancelled", "expired": "expired", "replaced": "replaced"}[outcome]
	}
	return appendExecution(ctx, tx, id, fact("card:"+c.ID+":"+strconv.Itoa(c.Version)+":"+outcome, "confirmation", nodeState, state, result, s.OperationalClock.Now()))
}
func (s *Store) confirmExecution(ctx context.Context, tx pgx.Tx, c actions.Card, a actions.Attempt) error {
	id, e := routeFor(ctx, tx, c.UserID, "card", c.ID)
	if e != nil || id == "" {
		return e
	}
	for _, link := range []execution.Link{{Kind: "attempt", ID: a.ID}, {Kind: "job", ID: a.JobID}} {
		if e = linkExecution(ctx, tx, id, c.UserID, link); e != nil {
			return e
		}
	}
	now := s.OperationalClock.Now()
	if e = appendExecution(ctx, tx, id, fact("attempt:"+a.ID+":"+a.JobID+":confirmed", "confirmation", "succeeded", "queued", "", now)); e != nil {
		return e
	}
	return appendExecution(ctx, tx, id, fact("attempt:"+a.ID+":"+a.JobID+":queued", "queue", "pending", "queued", "", now))
}
func (s *Store) attemptExecutionFact(ctx context.Context, tx pgx.Tx, job actions.Work, node, nodeState, state, result, key string) error {
	id, e := routeFor(ctx, tx, job.Attempt.UserID, "attempt", job.Attempt.ID)
	if e != nil || id == "" {
		return e
	}
	f := fact("attempt:"+job.Attempt.ID+":"+job.JobID+":"+key, node, nodeState, state, result, s.OperationalClock.Now())
	if node == "validation" {
		f.Node.DetailCode = "local"
	}
	if node == "service" {
		f.Node.DetailCode = "remote"
		f.Node.ServiceCode = "access"
		if job.CardType == "time_off_create" || job.CardType == "time_off_cancel" {
			f.Node.ServiceCode = "time_off"
		}
	}
	return appendExecution(ctx, tx, id, f)
}

// ObserveMessage guards intermediate facts with the same lease as the message worker.
func (s *Store) ObserveMessage(ctx context.Context, w MessageWork, node, nodeState string, keys ...string) (observationErr error) {
	defer func() {
		if observationErr != nil && !errors.Is(observationErr, actions.ErrLeaseLost) && w.ExecutionID != "" {
			mark, cancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
			defer cancel()
			_, _ = s.Pool.Exec(mark, `UPDATE execution_runs SET detail_complete=false,revision=revision+1 WHERE id=$1 AND user_id=$2 AND EXISTS(SELECT 1 FROM jobs WHERE id=$3 AND state='leased' AND lease_token::text=$4)`, w.ExecutionID, w.Message.UserID, w.JobID, w.LeaseToken)
		}
	}()

	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = lockWork(ctx, tx, actions.Work{JobID: w.JobID, LeaseToken: w.LeaseToken}); e != nil {
		return e
	}
	id, e := w.ExecutionID, error(nil)
	if id == "" {
		id, e = routeFor(ctx, tx, w.Message.UserID, "message", strconv.FormatInt(w.Message.ID, 10))
	}
	if e != nil || id == "" {
		return e
	}
	if node == "context" && nodeState == "running" {
		if e = appendExecution(ctx, tx, id, fact("job:"+w.JobID+":claimed", "queue", "succeeded", "running", "", s.OperationalClock.Now())); e != nil {
			return e
		}
	}
	key := "job:" + w.JobID + ":" + node + ":" + nodeState
	if len(keys) > 0 {
		key += ":" + keys[0]
	}
	if e = appendExecution(ctx, tx, id, fact(key, node, nodeState, "running", "", s.OperationalClock.Now())); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) completeMessageExecution(ctx context.Context, tx pgx.Tx, w MessageWork, messageID int64, code string, raw json.RawMessage) error {
	var dialogue struct {
		ExecutionID        string   `json:"execution_id"`
		Stage              string   `json:"stage"`
		DeclinedExecutions []string `json:"declined_executions"`
		Fallback           bool     `json:"fallback"`
	}
	if e := json.Unmarshal(raw, &dialogue); e != nil {
		return e
	}
	if e := declineExecutions(ctx, tx, w.Message.UserID, dialogue.DeclinedExecutions, "job:"+w.JobID, s.OperationalClock.Now()); e != nil {
		return e
	}
	source := w.ExecutionID
	if source == "" {
		var e error
		source, e = routeFor(ctx, tx, w.Message.UserID, "message", strconv.FormatInt(w.Message.ID, 10))
		if e != nil {
			return e
		}
	}
	target := dialogue.ExecutionID
	if target == "" {
		target = source
	}
	if target == "" {
		return nil
	}
	ids := []string{target}
	if source != "" && source != target {
		ids = append(ids, source)
	}
	for _, id := range ids {
		if e := linkExecution(ctx, tx, id, w.Message.UserID, execution.Link{Kind: "message", ID: strconv.FormatInt(messageID, 10)}); e != nil {
			return e
		}
		var routeState string
		if e := tx.QueryRow(ctx, `SELECT state FROM execution_runs WHERE id=$1`, id).Scan(&routeState); e != nil {
			return e
		}
		var hasAttempt bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_links WHERE run_id=$1 AND kind='attempt')`, id).Scan(&hasAttempt); e != nil {
			return e
		}
		// A response about a submitted operation never overrides that operation's durable outcome.
		if hasAttempt {
			continue
		}
		var cardState string
		cardErr := tx.QueryRow(ctx, `SELECT c.state FROM cards c JOIN execution_links l ON l.kind='card' AND l.object_id=c.id::text AND l.version=c.version WHERE l.run_id=$1 ORDER BY c.updated_at DESC,c.id DESC LIMIT 1`, id).Scan(&cardState)
		if cardErr != nil && cardErr != pgx.ErrNoRows {
			return cardErr
		}
		if cardState == "draft" {
			if e := appendExecution(ctx, tx, id, fact("job:"+w.JobID+":card_wait", "confirmation", "waiting_user", "waiting_user", "", s.OperationalClock.Now())); e != nil {
				return e
			}
			continue
		}
		if cardState == "discarded" || cardState == "expired" || cardState == "replaced" {
			result := map[string]string{"discarded": "cancelled", "expired": "expired", "replaced": "replaced"}[cardState]
			if e := appendExecution(ctx, tx, id, fact("job:"+w.JobID+":card_closed", "confirmation", "skipped", "cancelled", result, s.OperationalClock.Now())); e != nil {
				return e
			}
			continue
		}

		var hasChildren bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_links WHERE kind='operation_parent' AND object_id=$1)`, id).Scan(&hasChildren); e != nil {
			return e
		}
		if hasChildren {
			continue
		}
		if routeState == "cancelled" || routeState == "rejected" || routeState == "failed" || routeState == "completed" {
			continue
		}
		if id == target && routeState == "waiting_user" {
			continue
		}
		state, nodeState, result, node := "completed", "succeeded", "answer", "answer"
		if dialogue.Fallback {
			result = "fallback"
		}
		if code != "" {
			state, nodeState, result = "failed", "failed", ""
		} else if id == target && (dialogue.Stage == "parameters" || dialogue.Stage == "outside_access" || dialogue.Stage == "reapply" || dialogue.Stage == "confirm_step") {
			state, nodeState, result, node = "waiting_user", "waiting_user", "", "clarification"
		}
		if e := appendExecution(ctx, tx, id, fact("job:"+w.JobID+":answer", node, nodeState, state, result, s.OperationalClock.Now())); e != nil {
			return e
		}
	}
	return nil
}

// BindMessageRoute associates a continuation with its original logical request.
// The accepted processing route remains addressable and links to the original.
func (s *Store) BindMessageRoute(ctx context.Context, w MessageWork, target, scenario string) (string, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = lockWork(ctx, tx, actions.Work{JobID: w.JobID, LeaseToken: w.LeaseToken}); e != nil {
		return "", e
	}
	source := w.ExecutionID
	if source == "" {
		source, e = routeFor(ctx, tx, w.Message.UserID, "message", strconv.FormatInt(w.Message.ID, 10))
		if e != nil {
			return "", e
		}
	}
	if source == "" {
		return "", nil
	}
	if target == "" {
		target = source
	}
	var owned bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_runs WHERE id::text=$1 AND user_id=$2)`, target, w.Message.UserID).Scan(&owned); e != nil {
		return "", e
	}
	if !owned {
		return "", execution.ErrMissing
	}
	var submitted bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_links WHERE run_id=$1 AND kind='attempt')`, target).Scan(&submitted); e != nil {
		return "", e
	}
	if target != source {
		// Copy only facts actually observed for this continuation, preserving their provenance keys.
		rows, e := tx.Query(ctx, `SELECT fact_key,node_id,kind,node_state,detail_code,service_code,started_at,finished_at,observed_at FROM execution_events WHERE run_id=$1 AND kind IN ('context','model','sources') ORDER BY sequence`, source)
		if e != nil {
			return "", e
		}
		facts := []execution.Fact{}
		for rows.Next() {
			var f execution.Fact
			if e = rows.Scan(&f.Key, &f.Node.ID, &f.Node.Kind, &f.Node.State, &f.Node.DetailCode, &f.Node.ServiceCode, &f.Node.StartedAt, &f.Node.FinishedAt, &f.At); e != nil {
				rows.Close()
				return "", e
			}
			f.Node.LabelCode = f.Node.Kind
			f.State = "running"
			facts = append(facts, f)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
		for _, f := range facts {
			if submitted {
				continue
			}
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				return "", err
			}
			if err = appendExecution(ctx, savepoint, target, f); err != nil {
				_ = savepoint.Rollback(context.WithoutCancel(ctx))
				if _, err = tx.Exec(ctx, `UPDATE execution_runs SET detail_complete=false,revision=revision+1 WHERE id=$1`, target); err != nil {
					return "", err
				}
			} else if err = savepoint.Commit(ctx); err != nil {
				return "", err
			}
		}
		if e = linkExecution(ctx, tx, source, w.Message.UserID, execution.Link{Kind: "parent", ID: target}); e != nil {
			return "", e
		}
		for _, l := range []execution.Link{{Kind: "message", ID: strconv.FormatInt(w.Message.ID, 10)}, {Kind: "job", ID: w.JobID}} {
			if e = linkExecution(ctx, tx, target, w.Message.UserID, l); e != nil {
				return "", e
			}
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE execution_runs SET scenario=CASE WHEN scenario IN ('access','time_off') AND $2='orientation' THEN scenario ELSE $2 END WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM execution_links WHERE run_id=$1 AND kind='attempt')`, target, scenario); e != nil {
		return "", e
	}
	return target, tx.Commit(ctx)
}
func (s *Store) PendingExecution(ctx context.Context, w MessageWork, scenario string, ordinal int64) (string, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = lockWork(ctx, tx, actions.Work{JobID: w.JobID, LeaseToken: w.LeaseToken}); e != nil {
		return "", e
	}
	var existing string
	e = tx.QueryRow(ctx, `SELECT run_id::text FROM execution_links WHERE kind='message' AND user_id=$1 AND object_id=$2 AND version=$3 LIMIT 1`, w.Message.UserID, strconv.FormatInt(w.Message.ID, 10), ordinal).Scan(&existing)
	if e == nil {
		return existing, tx.Commit(ctx)
	}
	if e != pgx.ErrNoRows {
		return "", e
	}
	id := actions.ID()
	now := s.OperationalClock.Now()
	if e = createExecution(ctx, tx, execution.Run{ID: id, UserID: w.Message.UserID, Scenario: scenario, State: "waiting_user", StartedAt: now, DetailComplete: true, Links: []execution.Link{{Kind: "message", ID: strconv.FormatInt(w.Message.ID, 10), Version: ordinal}}}); e != nil {
		return "", e
	}
	if e = appendExecution(ctx, tx, id, fact("pending:"+id, "queue", "pending", "waiting_user", "", now)); e != nil {
		return "", e
	}
	return id, tx.Commit(ctx)
}

func (s *Store) TransferExecutions(ctx context.Context, w MessageWork, parent string) (string, string, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", "", e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = lockWork(ctx, tx, actions.Work{JobID: w.JobID, LeaseToken: w.LeaseToken}); e != nil {
		return "", "", e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM execution_runs WHERE id=$1 FOR UPDATE`, parent); e != nil {
		return "", "", e
	}
	rows, e := tx.Query(ctx, `SELECT run_id::text FROM execution_links WHERE kind='operation_parent' AND object_id=$1 ORDER BY version`, parent)
	if e != nil {
		return "", "", e
	}
	existing := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return "", "", e
		}
		existing = append(existing, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", "", e
	}
	if len(existing) == 2 {
		return existing[0], existing[1], tx.Commit(ctx)
	}
	ids := []string{actions.ID(), actions.ID()}
	now := s.OperationalClock.Now()
	for index, id := range ids {
		r := execution.Run{ID: id, UserID: w.Message.UserID, Scenario: "time_off", State: "waiting_user", StartedAt: now, DetailComplete: true, Links: []execution.Link{{Kind: "operation_parent", ID: parent, Version: int64(index + 1)}, {Kind: "message", ID: strconv.FormatInt(w.Message.ID, 10)}}}
		if e = createExecution(ctx, tx, r); e != nil {
			return "", "", e
		}
		if e = appendExecution(ctx, tx, id, fact("operation:"+id+":pending", "queue", "pending", "waiting_user", "", now)); e != nil {
			return "", "", e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE execution_runs SET scenario='transfer' WHERE id=$1 AND user_id=$2`, parent, w.Message.UserID); e != nil {
		return "", "", e
	}
	if e = appendExecution(ctx, tx, parent, fact("transfer:"+parent+":confirmation", "confirmation", "waiting_user", "waiting_user", "", now)); e != nil {
		return "", "", e
	}
	return ids[0], ids[1], tx.Commit(ctx)
}
func refreshTransfer(ctx context.Context, tx pgx.Tx, child string, f execution.Fact) error {
	var parent string
	e := tx.QueryRow(ctx, `SELECT object_id FROM execution_links WHERE run_id=$1 AND kind='operation_parent'`, child).Scan(&parent)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	// Serialize aggregate snapshots; children remain the authority for each operation.
	if _, e = tx.Exec(ctx, `SELECT id FROM execution_runs WHERE id=$1 FOR UPDATE`, parent); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT r.state FROM execution_runs r JOIN execution_links l ON l.run_id=r.id WHERE l.kind='operation_parent' AND l.object_id=$1 ORDER BY r.id`, parent)
	if e != nil {
		return e
	}
	states := []string{}
	for rows.Next() {
		var state string
		if e = rows.Scan(&state); e != nil {
			rows.Close()
			return e
		}
		states = append(states, state)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	state, nodeState, result := "waiting_user", "waiting_user", ""
	done := 0
	for _, s := range states {
		if s == "completed" {
			done++
		}
	}
	if done > 0 {
		result = "partial"
	}
	if len(states) == 2 && done == 2 {
		state, nodeState, result = "completed", "succeeded", "answer"
	} else {
		for _, s := range states {
			if s == "running" || s == "queued" {
				state, nodeState = "running", "running"
			}
		}
		for _, s := range states {
			if s == "rejected" || s == "failed" || s == "cancelled" {
				state, nodeState = s, "failed"
				if s == "rejected" {
					nodeState = "rejected"
				}
				if done == 0 {
					result = "rejected"
				}
			}
		}
		for _, s := range states {
			if s == "reconciling" || s == "manual_review" {
				state, nodeState, result = s, "unknown", "unknown"
				if s == "manual_review" {
					result = "manual_review"
				}
			}
		}
	}
	if f.Node.Kind == "confirmation" && f.Node.State == "succeeded" {
		received := fact("transfer:"+parent+":confirmation_received", "confirmation", "succeeded", state, result, f.At)
		if e = appendExecution(ctx, tx, parent, received); e != nil {
			return e
		}
	}
	parentFact := fact("child:"+child+":"+f.Key, "persistence", nodeState, state, result, f.At)
	return appendExecution(ctx, tx, parent, parentFact)
}

func declineExecutions(ctx context.Context, tx pgx.Tx, user string, ids []string, key string, now time.Time) error {
	for _, id := range ids {
		var eligible bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_runs r WHERE r.id::text=$1 AND r.user_id=$2 AND r.state IN ('waiting_user','running','queued') AND NOT EXISTS(SELECT 1 FROM execution_links l WHERE l.run_id=r.id AND l.kind='attempt'))`, id, user).Scan(&eligible); e != nil {
			return e
		}
		if !eligible {
			continue
		}
		if e := appendExecution(ctx, tx, id, fact(key+":declined:"+id, "clarification", "skipped", "cancelled", "cancelled", now)); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) ObserveCardRefusal(ctx context.Context, user, card string, version int) error {
	if !actions.ValidID(card) {
		return actions.ErrMissing
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	id, e := routeFor(ctx, tx, user, "card", card)
	if e != nil || id == "" {
		return e
	}
	var waiting bool
	if e = tx.QueryRow(ctx, `SELECT state='waiting_user' FROM execution_runs WHERE id=$1 AND user_id=$2`, id, user).Scan(&waiting); e != nil {
		return e
	}
	if !waiting {
		return nil
	}
	f := fact("card:"+card+":"+strconv.Itoa(version)+":refused", "validation", "rejected", "waiting_user", "", s.OperationalClock.Now())
	f.Node.DetailCode = "local"
	if e = appendExecution(ctx, tx, id, f); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
