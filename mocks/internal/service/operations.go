package service

import (
	"ai-buddy/backend/platform/demo"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type operationResult struct {
	Status int
	Body   []byte
	Kind   string
}

func rawResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(append([]byte(nil), body...), '\n'))
}
func rejected(status int, code string) operationResult {
	data, _ := json.Marshal(problem(code))
	return operationResult{status, data, "rejected"}
}
func succeeded(status int, object any) operationResult {
	data, _ := json.Marshal(object)
	return operationResult{status, data, "success"}
}
func (s *Server) visibleResult(ctx context.Context, tx pgx.Tx, p demo.Profile, result operationResult) bool {
	if result.Kind != "success" {
		return true
	}
	var object struct {
		UserID     string `json:"user_id"`
		ResourceID string `json:"resource_id"`
	}
	if json.Unmarshal(result.Body, &object) != nil || object.UserID != p.ID {
		return false
	}
	if object.ResourceID != "" {
		var data []byte
		err := tx.QueryRow(ctx, `SELECT payload FROM resources WHERE id=$1`, object.ResourceID).Scan(&data)
		var resource demo.Resource
		if err == nil {
			err = json.Unmarshal(data, &resource)
		}
		if err != nil || resource.Kind == "hidden" {
			return false
		}
	}
	return true
}
func (s *Server) operation(ctx context.Context, p demo.Profile, key, kind string, canonical []byte, business func(pgx.Tx, demo.Profile) (operationResult, error)) (operationResult, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return operationResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var actorData []byte
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT payload,enabled FROM actors WHERE id=$1 FOR UPDATE`, p.ID).Scan(&actorData, &enabled); err != nil {
		return operationResult{}, err
	}
	if json.Unmarshal(actorData, &p) != nil {
		return operationResult{}, errSchema
	}
	p.Enabled = enabled
	if !s.canAct(p) {
		return rejected(403, "NOT_ALLOWED"), nil
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return operationResult{}, err
	}
	digest := sha256.Sum256(append([]byte(kind+":"), canonical...))
	var owner, state string
	var previousHash []byte
	var result operationResult
	err = tx.QueryRow(ctx, `SELECT actor_id,state,body_hash,COALESCE(http_status,0),COALESCE(body,''::bytea),COALESCE(result_kind,'') FROM operations WHERE operation_key=$1`, key).Scan(&owner, &state, &previousHash, &result.Status, &result.Body, &result.Kind)
	if err == nil {
		if owner != p.ID {
			return rejected(404, "NOT_FOUND"), nil
		}
		if state == "not_created" {
			return rejected(409, "OPERATION_CLOSED"), nil
		}
		if !bytes.Equal(previousHash, digest[:]) {
			return rejected(409, "IDEMPOTENCY_CONFLICT"), nil
		}
		if !s.visibleResult(ctx, tx, p, result) {
			return rejected(404, "NOT_FOUND"), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return result, errCommitUnknown
		}
		return result, nil
	}
	if err != pgx.ErrNoRows {
		return operationResult{}, err
	}
	result, err = business(tx, p)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO operations(operation_key,actor_id,body_hash,state,result_kind,http_status,body) VALUES($1,$2,$3,'completed',$4,$5,$6)`, key, p.ID, digest[:], result.Kind, result.Status, result.Body); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, errCommitUnknown
	}
	return result, nil
}

var errSchema = errors.New("MOCK_SCHEMA_INVALID")
var errCommitUnknown = errors.New("COMMIT_OUTCOME_UNKNOWN")

func (s *Server) fault(ctx context.Context, service string) (string, time.Duration, error) {
	var mode string
	var delay int
	err := s.Store.Pool.QueryRow(ctx, `SELECT mode,delay_ms FROM faults WHERE service=$1`, service).Scan(&mode, &delay)
	return mode, time.Duration(delay) * time.Millisecond, err
}
func (s *Server) before(w http.ResponseWriter, r *http.Request, service string) (string, bool) {
	mode, delay, err := s.fault(r.Context(), service)
	if err != nil || mode == "unavailable_before" {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return mode, false
	}
	if mode == "delay_before" {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return mode, false
		case <-timer.C:
		}
	}
	return mode, true
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	key := canonicalKey(r.PathValue("key"))
	if !validKey(key) {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	mode, _, err := s.fault(r.Context(), "access")
	if err != nil || mode == "reconciliation_unavailable" {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer tx.Rollback(context.WithoutCancel(r.Context()))
	if _, err = tx.Exec(r.Context(), `SELECT id FROM actors WHERE id=$1 FOR UPDATE`, p.ID); err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	var owner, state string
	var result operationResult
	err = tx.QueryRow(r.Context(), `SELECT actor_id,state,COALESCE(http_status,0),COALESCE(body,''::bytea),COALESCE(result_kind,'') FROM operations WHERE operation_key=$1`, key).Scan(&owner, &state, &result.Status, &result.Body, &result.Kind)
	if err == pgx.ErrNoRows {
		if _, err = tx.Exec(r.Context(), `INSERT INTO operations(operation_key,actor_id,state) VALUES($1,$2,'not_created')`, key, p.ID); err != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		state = "not_created"
	} else if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	} else if owner != p.ID {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if state == "completed" && !s.visibleResult(r.Context(), tx, p, result) {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	output := map[string]any{"state": state, "operation_key": key}
	if state == "completed" {
		output["result"] = map[string]any{"kind": result.Kind, "http_status": result.Status, "body": json.RawMessage(result.Body)}
	}
	response(w, 200, output)
}
