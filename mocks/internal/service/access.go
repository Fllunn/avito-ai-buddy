package service

import (
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"
)

var resourceID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var levelID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type accessInput struct {
	Key           string `json:"operation_key"`
	ResourceID    string `json:"resource_id"`
	Level         string `json:"level"`
	Justification string `json:"justification"`
}
type cancelInput struct {
	Key     string `json:"operation_key"`
	Version int64  `json:"expected_version"`
}

func (s *Server) createAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	if !s.canAct(p) {
		failure(w, 403, "NOT_ALLOWED")
		return
	}
	mode, ok := s.before(w, r, "access")
	if !ok {
		return
	}
	var input accessInput
	if !decode(w, r, &input) {
		return
	}
	input.Key = canonicalKey(input.Key)
	if !validKey(input.Key) || input.ResourceID == "" || input.Level == "" || input.Justification == "" {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	if !resourceID.MatchString(input.ResourceID) || !levelID.MatchString(input.Level) {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	for _, value := range []string{input.ResourceID, input.Level} {
		safe := safety.Prepare(r.Context(), safety.Local{}, value)
		if safe.UseForm() || safe.String() != value {
			failure(w, 400, "VALIDATION_ERROR")
			return
		}
	}
	cleaned := safety.Prepare(r.Context(), safety.Local{}, input.Justification)
	input.Justification = cleaned.String()
	canonical, _ := json.Marshal(input)
	result, err := s.operation(r.Context(), p, input.Key, "access_create", canonical, func(tx pgx.Tx, p demo.Profile) (operationResult, error) {
		if cleaned.UseForm() || utf8.RuneCountInString(input.Justification) < 10 || utf8.RuneCountInString(input.Justification) > 500 {
			return rejected(400, "VALIDATION_ERROR"), nil
		}
		var data []byte
		err := tx.QueryRow(r.Context(), `SELECT payload FROM resources WHERE id=$1`, input.ResourceID).Scan(&data)
		if err == pgx.ErrNoRows {
			return rejected(404, "NOT_FOUND"), nil
		}
		if err != nil {
			return operationResult{}, err
		}
		var resource demo.Resource
		if json.Unmarshal(data, &resource) != nil {
			return operationResult{}, errSchema
		}
		if resource.Kind == "hidden" {
			return rejected(404, "NOT_FOUND"), nil
		}
		if resource.Kind == "closed" {
			return rejected(403, "NOT_ALLOWED"), nil
		}
		if !slices.Contains(resource.Levels, input.Level) {
			return rejected(409, "LEVEL_NOT_ALLOWED"), nil
		}
		var granted bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM grants WHERE user_id=$1 AND resource_id=$2)`, p.ID, input.ResourceID).Scan(&granted); err != nil {
			return operationResult{}, err
		}
		if granted {
			return rejected(409, "ALREADY_GRANTED"), nil
		}
		var existing string
		err = tx.QueryRow(r.Context(), `SELECT id FROM access_requests WHERE user_id=$1 AND resource_id=$2 AND status IN ('created','in_review')`, p.ID, input.ResourceID).Scan(&existing)
		if err == nil {
			body := problem("DUPLICATE_OPEN_REQUEST")
			body.ExistingID = existing
			data, _ := json.Marshal(body)
			return operationResult{409, data, "rejected"}, nil
		}
		if err != pgx.ErrNoRows {
			return operationResult{}, err
		}
		approver := resource.ApproverID
		if approver == "manager" {
			if p.ManagerID == nil {
				return rejected(403, "NOT_ALLOWED"), nil
			}
			approver = *p.ManagerID
		}
		var number int64
		if err := tx.QueryRow(r.Context(), `SELECT nextval('access_number')`).Scan(&number); err != nil {
			return operationResult{}, err
		}
		now := s.Config.Clock.Now()
		item := AccessRequest{ID: "ACC-" + strconv.FormatInt(number, 10), UserID: p.ID, ResourceID: resource.ID, Level: input.Level, Justification: input.Justification, ApproverID: approver, Status: "created", Version: 1, CreatedAt: now, UpdatedAt: now}
		_, err = tx.Exec(r.Context(), `INSERT INTO access_requests(id,user_id,resource_id,level,justification,approver_id,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'created',1,$7,$7)`, item.ID, p.ID, item.ResourceID, item.Level, item.Justification, item.ApproverID, now)
		return succeeded(201, item), err
	})
	if err != nil {
		if errors.Is(err, errCommitUnknown) {
			failure(w, 500, "SERVICE_ERROR")
		} else {
			failure(w, 503, "SERVICE_UNAVAILABLE")
		}
		return
	}
	if mode == "lose_response_after" {
		panic(http.ErrAbortHandler)
	}
	rawResponse(w, result.Status, result.Body)
}
func (s *Server) cancelAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	if !s.canAct(p) {
		failure(w, 403, "NOT_ALLOWED")
		return
	}
	mode, ok := s.before(w, r, "access")
	if !ok {
		return
	}
	var input cancelInput
	if !decode(w, r, &input) {
		return
	}
	input.Key = canonicalKey(input.Key)
	if !validKey(input.Key) || input.Version < 1 {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	id := r.PathValue("id")
	if len(id) < 5 || id[:4] != "ACC-" {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if _, err := strconv.ParseUint(id[4:], 10, 64); err != nil {
		failure(w, 404, "NOT_FOUND")
		return
	}
	canonical, _ := json.Marshal(input)
	result, err := s.operation(r.Context(), p, input.Key, "access_cancel:"+id, canonical, func(tx pgx.Tx, p demo.Profile) (operationResult, error) {
		item, err := scanAccess(tx.QueryRow(r.Context(), `SELECT `+accessColumns+` FROM access_requests WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, p.ID))
		if err == pgx.ErrNoRows {
			return rejected(404, "NOT_FOUND"), nil
		}
		if err != nil {
			return operationResult{}, err
		}
		var payload []byte
		if err := tx.QueryRow(r.Context(), `SELECT payload FROM resources WHERE id=$1`, item.ResourceID).Scan(&payload); err != nil {
			return operationResult{}, err
		}
		var resource demo.Resource
		if json.Unmarshal(payload, &resource) != nil {
			return operationResult{}, errSchema
		}
		if resource.Kind == "hidden" {
			return rejected(404, "NOT_FOUND"), nil
		}
		if item.Version != input.Version {
			return rejected(409, "VERSION_CONFLICT"), nil
		}
		if item.Status != "created" && item.Status != "in_review" {
			return rejected(409, "INVALID_STATE"), nil
		}
		item.Version++
		item.Status = "cancelled"
		item.UpdatedAt = s.Config.Clock.Now()
		_, err = tx.Exec(r.Context(), `UPDATE access_requests SET status='cancelled',version=$2,updated_at=$3 WHERE id=$1`, id, item.Version, item.UpdatedAt)
		return succeeded(200, item), err
	})
	if err != nil {
		if errors.Is(err, errCommitUnknown) {
			failure(w, 500, "SERVICE_ERROR")
		} else {
			failure(w, 503, "SERVICE_UNAVAILABLE")
		}
		return
	}
	if mode == "lose_response_after" {
		panic(http.ErrAbortHandler)
	}
	rawResponse(w, result.Status, result.Body)
}
