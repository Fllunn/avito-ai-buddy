package service

import (
	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/health"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Secret           string
	PanelSecret      string
	PanelOrigin      string
	OperationalClock clock.Clock
	Clock            clock.Clock
	Location         *time.Location
	Demo             bool
}
type Server struct {
	Store  *Store
	Config Config
}

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.Store.Pool.Ping(r.Context()) != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		response(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /v1/time-off-rules", s.timeOffRules)
	mux.HandleFunc("GET /v1/time-off-requests", s.listTimeOff)
	mux.HandleFunc("GET /v1/time-off-requests/{id}", s.getTimeOff)
	mux.HandleFunc("POST /v1/time-off-requests", s.createTimeOff)
	mux.HandleFunc("POST /v1/time-off-requests/{id}/cancel", s.cancelTimeOff)
	mux.HandleFunc("GET /v1/resources", s.resources)
	mux.HandleFunc("GET /v1/grants", s.grants)
	mux.HandleFunc("GET /v1/access-requests", s.listAccess)
	mux.HandleFunc("GET /v1/access-requests/{id}", s.getAccess)
	mux.HandleFunc("POST /v1/access-requests", s.createAccess)
	mux.HandleFunc("POST /v1/access-requests/{id}/cancel", s.cancelAccess)
	mux.HandleFunc("POST /v1/operations/{key}/reconcile", s.reconcile)
	if s.Config.Demo {
		s.panelRoutes(mux)
	}
	mux.Handle("/", health.Handler("mocks"))
	return mux
}
func response(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, code string) { response(w, status, problem(code)) }
func (s *Server) actor(w http.ResponseWriter, r *http.Request) (demo.Profile, bool) {
	expected := sha256.Sum256([]byte("Bearer " + s.Config.Secret))
	actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if len(s.Config.Secret) < 32 || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 || r.Header.Get("Origin") != "" {
		failure(w, 401, "UNAUTHENTICATED")
		return demo.Profile{}, false
	}
	p, err := s.Store.Actor(r.Context(), r.Header.Get("X-Actor-ID"))
	if err != nil {
		if err == pgx.ErrNoRows {
			failure(w, 401, "UNAUTHENTICATED")
		} else {
			failure(w, 503, "SERVICE_UNAVAILABLE")
		}
		return demo.Profile{}, false
	}
	if !p.Enabled {
		failure(w, 401, "UNAUTHENTICATED")
		return demo.Profile{}, false
	}
	return p, true
}
func (s *Server) canAct(p demo.Profile) bool {
	return p.Role == "intern" && p.Enabled && s.Config.Clock.Now().In(s.Config.Location).Format(time.DateOnly) >= p.StartDate
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, 400, "VALIDATION_ERROR")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		failure(w, 400, "VALIDATION_ERROR")
		return false
	}
	return true
}
func (s *Server) resource(ctx context.Context, id string) (demo.Resource, error) {
	var data []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT payload FROM resources WHERE id=$1`, id).Scan(&data)
	var item demo.Resource
	if err == nil {
		err = json.Unmarshal(data, &item)
	}
	return item, err
}
func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT payload FROM resources ORDER BY id`)
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer rows.Close()
	result := []Resource{}
	for rows.Next() {
		var data []byte
		var item demo.Resource
		if rows.Scan(&data) != nil || json.Unmarshal(data, &item) != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		if item.Kind == "hidden" {
			continue
		}
		var level, approver *string
		if item.DefaultLevel != "" {
			level = &item.DefaultLevel
		}
		owner := item.ApproverID
		if owner == "manager" {
			owner = ""
			if p.ManagerID != nil {
				owner = *p.ManagerID
			}
		}
		if owner != "" {
			approver = &owner
		}
		if item.Kind == "closed" {
			level = nil
			approver = nil
		}
		result = append(result, Resource{item.ID, item.Name, item.Synonyms, s.canAct(p) && item.Kind != "closed" && approver != nil, item.Levels, level, item.RecommendedTeams, item.Kind == "task", approver})
	}
	if rows.Err() != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	response(w, 200, map[string]any{"items": result})
}

var accessSelect = "a." + strings.ReplaceAll(accessColumns, ",", ",a.")

const accessColumns = `id,user_id,resource_id,level,justification,approver_id,status,reason,version,created_at,updated_at`

type row interface{ Scan(...any) error }

func scanAccess(row row) (AccessRequest, error) {
	var item AccessRequest
	err := row.Scan(&item.ID, &item.UserID, &item.ResourceID, &item.Level, &item.Justification, &item.ApproverID, &item.Status, &item.Reason, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
func (s *Server) listAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	resource := r.URL.Query().Get("resource_id")
	if resource != "" {
		item, err := s.resource(r.Context(), resource)
		if err != nil || item.Kind == "hidden" {
			failure(w, 404, "NOT_FOUND")
			return
		}
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT `+accessSelect+` FROM access_requests a JOIN resources res ON res.id=a.resource_id WHERE a.user_id=$1 AND ($2='' OR a.resource_id=$2) AND res.payload->>'kind'<>'hidden' ORDER BY a.created_at,a.id`, p.ID, resource)
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer rows.Close()
	items := []AccessRequest{}
	for rows.Next() {
		item, err := scanAccess(rows)
		if err != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	response(w, 200, map[string]any{"items": items})
}
func (s *Server) getAccess(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	item, err := scanAccess(s.Store.Pool.QueryRow(r.Context(), `SELECT `+accessSelect+` FROM access_requests a JOIN resources res ON res.id=a.resource_id WHERE a.id=$1 AND a.user_id=$2 AND res.payload->>'kind'<>'hidden'`, r.PathValue("id"), p.ID))
	if err != nil {
		failure(w, 404, "NOT_FOUND")
		return
	}
	response(w, 200, item)
}
func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	p, ok := s.actor(w, r)
	if !ok {
		return
	}
	resource := r.URL.Query().Get("resource_id")
	if resource != "" {
		item, err := s.resource(r.Context(), resource)
		if err != nil || item.Kind == "hidden" {
			failure(w, 404, "NOT_FOUND")
			return
		}
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT g.resource_id,g.level,g.granted_at,g.source_request_id FROM grants g JOIN resources res ON res.id=g.resource_id WHERE g.user_id=$1 AND ($2='' OR g.resource_id=$2) AND res.payload->>'kind'<>'hidden' ORDER BY g.resource_id`, p.ID, resource)
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer rows.Close()
	items := []Grant{}
	for rows.Next() {
		var g Grant
		if rows.Scan(&g.ResourceID, &g.Level, &g.GrantedAt, &g.SourceRequestID) != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
		items = append(items, g)
	}
	if rows.Err() != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	response(w, 200, map[string]any{"items": items})
}
func validKey(key string) bool       { return uuid.MatchString(key) }
func canonicalKey(key string) string { return strings.ToLower(key) }
