package service

import (
	"ai-buddy/backend/platform/calendar"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

type TimeOffRequest struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	StartDate  string    `json:"start_date"`
	EndDate    string    `json:"end_date"`
	Workdays   int       `json:"workdays"`
	ApproverID string    `json:"approver_id"`
	Status     string    `json:"status"`
	Reason     *string   `json:"reason"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const absenceColumns = `id,user_id,to_char(start_date,'YYYY-MM-DD'),to_char(end_date,'YYYY-MM-DD'),workdays,approver_id,status,reason,version,created_at,updated_at`

func scanAbsence(r row) (TimeOffRequest, error) {
	var a TimeOffRequest
	e := r.Scan(&a.ID, &a.UserID, &a.StartDate, &a.EndDate, &a.Workdays, &a.ApproverID, &a.Status, &a.Reason, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, e
}
func (s *Server) timeOffRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.actor(w, r); !ok {
		return
	}
	response(w, 200, map[string]any{"min_notice_workdays": 1, "max_workdays": 5, "horizon_days": 90, "timezone": s.Config.Location.String(), "holidays": calendar.Holidays})
}
func (s *Server) listTimeOff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	rows, e := s.Store.Pool.Query(r.Context(), `SELECT `+absenceColumns+` FROM time_off_requests WHERE user_id=$1 ORDER BY created_at,id`, p.ID)
	if e != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer rows.Close()
	items := []TimeOffRequest{}
	for rows.Next() {
		a, e := scanAbsence(rows)
		if e != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		items = append(items, a)
	}
	if rows.Err() != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	response(w, 200, map[string]any{"items": items})
}
func (s *Server) getTimeOff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	a, e := scanAbsence(s.Store.Pool.QueryRow(r.Context(), `SELECT `+absenceColumns+` FROM time_off_requests WHERE id=$1 AND user_id=$2`, r.PathValue("id"), p.ID))
	if e == pgx.ErrNoRows {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if e != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	response(w, 200, a)
}
func (s *Server) createTimeOff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	if !s.canAct(p) {
		failure(w, 403, "NOT_ALLOWED")
		return
	}
	mode, ok := s.before(w, r, "time_off")
	if !ok {
		return
	}
	var input struct {
		Key   string `json:"operation_key"`
		Start string `json:"start_date"`
		End   string `json:"end_date"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Key = canonicalKey(input.Key)
	if !validKey(input.Key) || input.Start == "" || input.End == "" {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	body, _ := json.Marshal(input)
	result, e := s.operation(r.Context(), p, input.Key, "time_off_create", body, func(tx pgx.Tx, p demo.Profile) (operationResult, error) {
		days, fields := calendar.Validate(s.Config.Clock.Now(), s.Config.Location, input.Start, input.End)
		if len(fields) > 0 {
			problem := problem("VALIDATION_ERROR")
			problem.Error.Fields = fields
			data, _ := json.Marshal(problem)
			return operationResult{400, data, "rejected"}, nil
		}
		if p.ManagerID == nil {
			return rejected(403, "NOT_ALLOWED"), nil
		}
		var manager bool
		if e := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM actors WHERE id=$1 AND enabled AND payload->>'role'='manager')`, *p.ManagerID).Scan(&manager); e != nil {
			return operationResult{}, e
		}
		if !manager {
			return rejected(403, "NOT_ALLOWED"), nil
		}
		var existing string
		e := tx.QueryRow(r.Context(), `SELECT id FROM time_off_requests WHERE user_id=$1 AND status IN ('submitted','in_review','approved') AND start_date<=$3::date AND end_date>=$2::date LIMIT 1`, p.ID, input.Start, input.End).Scan(&existing)
		if e == nil {
			problem := problem("OVERLAPPING_REQUEST")
			problem.ExistingID = existing
			data, _ := json.Marshal(problem)
			return operationResult{409, data, "rejected"}, nil
		}
		if e != pgx.ErrNoRows {
			return operationResult{}, e
		}
		var number int64
		if e := tx.QueryRow(r.Context(), `SELECT nextval('absence_number')`).Scan(&number); e != nil {
			return operationResult{}, e
		}
		now := s.Config.Clock.Now()
		a := TimeOffRequest{ID: "ABS-" + strconv.FormatInt(number, 10), UserID: p.ID, StartDate: input.Start, EndDate: input.End, Workdays: days, ApproverID: *p.ManagerID, Status: "submitted", Version: 1, CreatedAt: now, UpdatedAt: now}
		_, e = tx.Exec(r.Context(), `INSERT INTO time_off_requests(id,user_id,start_date,end_date,workdays,approver_id,status,version,created_at,updated_at) VALUES($1,$2,$3::date,$4::date,$5,$6,'submitted',1,$7,$7)`, a.ID, a.UserID, a.StartDate, a.EndDate, a.Workdays, a.ApproverID, now)
		return succeeded(201, a), e
	})
	s.operationReply(w, result, e, mode)
}
func (s *Server) operationReply(w http.ResponseWriter, result operationResult, e error, mode string) {
	if e != nil {
		if errors.Is(e, errCommitUnknown) {
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
func (s *Server) cancelTimeOff(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	if !s.canAct(p) {
		failure(w, 403, "NOT_ALLOWED")
		return
	}
	mode, ok := s.before(w, r, "time_off")
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
	body, _ := json.Marshal(input)
	result, e := s.operation(r.Context(), p, input.Key, "time_off_cancel:"+id, body, func(tx pgx.Tx, p demo.Profile) (operationResult, error) {
		a, e := scanAbsence(tx.QueryRow(r.Context(), `SELECT `+absenceColumns+` FROM time_off_requests WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, p.ID))
		if e == pgx.ErrNoRows {
			return rejected(404, "NOT_FOUND"), nil
		}
		if e != nil {
			return operationResult{}, e
		}
		if a.Version != input.Version {
			return rejected(409, "VERSION_CONFLICT"), nil
		}
		if s.Config.Clock.Now().In(s.Config.Location).Format(time.DateOnly) >= a.StartDate {
			return rejected(409, "CANCELLATION_TOO_LATE"), nil
		}
		if a.Status != "submitted" && a.Status != "in_review" && a.Status != "approved" {
			return rejected(409, "INVALID_STATE"), nil
		}
		a.Version++
		a.Status = "cancelled"
		a.UpdatedAt = s.Config.Clock.Now()
		_, e = tx.Exec(r.Context(), `UPDATE time_off_requests SET status='cancelled',version=$2,updated_at=$3 WHERE id=$1`, id, a.Version, a.UpdatedAt)
		return succeeded(200, a), e
	})
	s.operationReply(w, result, e, mode)
}
func (s *Server) TimeOffStatus(ctx context.Context, id string, expected int64, status, reason string) (operationResult, error) {
	if expected < 1 || status != "in_review" && status != "approved" && status != "rejected" && status != "cancelled" {
		return rejected(400, "VALIDATION_ERROR"), nil
	}
	clean := safety.Prepare(ctx, safety.Local{}, reason).String()
	if status == "rejected" && (utf8.RuneCountInString(clean) < 1 || utf8.RuneCountInString(clean) > 500) {
		return rejected(400, "VALIDATION_ERROR"), nil
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return operationResult{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var owner string
	e = tx.QueryRow(ctx, `SELECT user_id FROM time_off_requests WHERE id=$1`, id).Scan(&owner)
	if e == pgx.ErrNoRows {
		return rejected(404, "NOT_FOUND"), nil
	}
	if e != nil {
		return operationResult{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT id FROM actors WHERE id=$1 FOR UPDATE`, owner); e != nil {
		return operationResult{}, e
	}
	a, e := scanAbsence(tx.QueryRow(ctx, `SELECT `+absenceColumns+` FROM time_off_requests WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return operationResult{}, e
	}
	if a.Version != expected {
		return rejected(409, "VERSION_CONFLICT"), nil
	}
	if a.Status == "cancelled" || a.Status == "rejected" || a.Status == status || a.Status == "approved" && status != "cancelled" {
		return rejected(409, "INVALID_STATE"), nil
	}
	if status == "cancelled" && s.Config.Clock.Now().In(s.Config.Location).Format(time.DateOnly) >= a.StartDate {
		return rejected(409, "CANCELLATION_TOO_LATE"), nil
	}
	a.Version++
	a.Status = status
	a.UpdatedAt = s.Config.Clock.Now()
	a.Reason = nil
	if status == "rejected" {
		a.Reason = &clean
	}
	if _, e = tx.Exec(ctx, `UPDATE time_off_requests SET status=$2,reason=$3,version=$4,updated_at=$5 WHERE id=$1`, id, status, a.Reason, a.Version, a.UpdatedAt); e != nil {
		return operationResult{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return operationResult{}, errCommitUnknown
	}
	return succeeded(200, a), nil
}
func (s *Server) panelTimeOffStatus(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	var input statusInput
	if !decode(w, r, &input) {
		return
	}
	result, e := s.TimeOffStatus(r.Context(), r.PathValue("id"), input.Version, input.Status, input.Reason)
	s.operationReply(w, result, e, "normal")
}
