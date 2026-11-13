package service

import (
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"unicode/utf8"
)

// AccessStatus is reserved for the separately authenticated presenter panel.
func (s *Server) AccessStatus(ctx context.Context, id string, expected int64, status, reason string) (operationResult, error) {
	if expected < 1 || status != "in_review" && status != "approved" && status != "rejected" && status != "cancelled" {
		return rejected(400, "VALIDATION_ERROR"), nil
	}
	cleaned := safety.Prepare(ctx, safety.Local{}, reason).String()
	if status == "rejected" && (utf8.RuneCountInString(cleaned) < 1 || utf8.RuneCountInString(cleaned) > 500) {
		return rejected(400, "VALIDATION_ERROR"), nil
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return operationResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var owner string
	err = tx.QueryRow(ctx, `SELECT user_id FROM access_requests WHERE id=$1`, id).Scan(&owner)
	if err == pgx.ErrNoRows {
		return rejected(404, "NOT_FOUND"), nil
	}
	if err != nil {
		return operationResult{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM actors WHERE id=$1 FOR UPDATE`, owner); err != nil {
		return operationResult{}, err
	}
	item, err := scanAccess(tx.QueryRow(ctx, `SELECT `+accessColumns+` FROM access_requests WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return operationResult{}, err
	}
	if item.Version != expected {
		return rejected(409, "VERSION_CONFLICT"), nil
	}
	if item.Status != "created" && item.Status != "in_review" || item.Status == status {
		return rejected(409, "INVALID_STATE"), nil
	}
	item.Status = status
	item.Version++
	item.UpdatedAt = s.Config.Clock.Now()
	item.Reason = nil
	if status == "rejected" {
		item.Reason = &cleaned
	}
	if _, err = tx.Exec(ctx, `UPDATE access_requests SET status=$2,version=$3,reason=$4,updated_at=$5 WHERE id=$1`, id, status, item.Version, item.Reason, item.UpdatedAt); err != nil {
		return operationResult{}, err
	}
	if status == "approved" {
		if _, err = tx.Exec(ctx, `INSERT INTO grants(user_id,resource_id,level,granted_at,source_request_id) VALUES($1,$2,$3,$4,$5)`, item.UserID, item.ResourceID, item.Level, item.UpdatedAt, item.ID); err != nil {
			return operationResult{}, err
		}
	}
	body, err := json.Marshal(item)
	if err != nil {
		return operationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return operationResult{}, errCommitUnknown
	}
	return operationResult{200, body, "success"}, nil
}
