package api

import (
	"ai-buddy/backend/internal/execution"
	"ai-buddy/backend/internal/storage"
	"errors"
	"net/http"
	"strconv"
)

func executionLimit(r *http.Request) (int, error) {
	values, ok := r.URL.Query()["limit"]
	if !ok {
		return 20, nil
	}
	if len(values) != 1 {
		return 0, execution.ErrInvalid
	}
	n, e := strconv.Atoi(values[0])
	if e != nil || n < 1 || n > 100 {
		return 0, execution.ErrInvalid
	}
	return n, nil
}
func (s *Server) executionFailure(w http.ResponseWriter, e error) {
	if errors.Is(e, execution.ErrMissing) {
		failure(w, 404, "NOT_FOUND")
	} else if errors.Is(e, execution.ErrInvalid) {
		failure(w, 400, "VALIDATION_ERROR")
	} else {
		failure(w, 503, "STORAGE_UNAVAILABLE")
	}
}
func (s *Server) executions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	limit, e := executionLimit(r)
	if e != nil {
		s.executionFailure(w, e)
		return
	}
	q := r.URL.Query()
	active := false
	for key, values := range q {
		if len(values) != 1 || (key != "cursor" && key != "limit" && key != "active") {
			s.executionFailure(w, execution.ErrInvalid)
			return
		}
	}
	if values, ok := q["active"]; ok {
		if values[0] != "true" && values[0] != "false" {
			s.executionFailure(w, execution.ErrInvalid)
			return
		}
		active = values[0] == "true"
	}
	if values, ok := q["cursor"]; ok && values[0] == "" {
		s.executionFailure(w, execution.ErrInvalid)
		return
	}
	result, e := s.config.Business.Store.Executions(r.Context(), p.ID, q.Get("cursor"), limit, active)
	if e != nil {
		s.executionFailure(w, e)
		return
	}
	respond(w, 200, result)
}
func (s *Server) execution(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	limit, e := executionLimit(r)
	if e != nil {
		s.executionFailure(w, e)
		return
	}
	q := r.URL.Query()
	var after int64
	for key, values := range q {
		if len(values) != 1 || (key != "after_sequence" && key != "limit") {
			s.executionFailure(w, execution.ErrInvalid)
			return
		}
	}
	if values, ok := q["after_sequence"]; ok {
		after, e = strconv.ParseInt(values[0], 10, 64)
		if e != nil || after < 0 {
			s.executionFailure(w, execution.ErrInvalid)
			return
		}
	}
	result, e := (storage.ExecutionRepository{Store: s.config.Business.Store}).Read(r.Context(), p.ID, r.PathValue("id"), after, limit)
	if e != nil {
		s.executionFailure(w, e)
		return
	}
	respond(w, 200, result)
}
