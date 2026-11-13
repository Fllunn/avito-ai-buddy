package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"ai-buddy/backend/platform/clock"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/health"
)

const cookieName = "buddy_session"

type IdentityStore interface {
	Profile(context.Context, string) (demo.Profile, error)
	ReplaceSession(context.Context, string, string, string, time.Time) error
	SessionProfile(context.Context, string, time.Time) (demo.Profile, error)
	DeleteSession(context.Context, string) error
}
type Config struct {
	Demo       bool
	Origin     string
	Clock      clock.Clock
	SessionTTL time.Duration
	Location   *time.Location
}
type Server struct {
	identities IdentityStore
	config     Config
}

func New(identities IdentityStore, config Config) http.Handler {
	s := &Server{identities, config}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/demo/session", s.login)
	mux.HandleFunc("DELETE /api/session", s.logout)
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/onboarding", s.onboarding)
	mux.Handle("/", health.Handler("api"))
	return s.guard(mux)
}
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		origin := r.Header.Get("Origin")
		if origin != "" && origin != s.config.Origin {
			failure(w, 403, "ORIGIN_NOT_ALLOWED")
			return
		}
		if origin == s.config.Origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				failure(w, 403, "ORIGIN_NOT_ALLOWED")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && origin == "" {
			failure(w, 403, "ORIGIN_NOT_ALLOWED")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		failure(w, 415, "JSON_REQUIRED")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		failure(w, 400, "VALIDATION_ERROR")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]any{"code": code, "fields": map[string]string{}, "retryable": status == 503}, "request_id": actions.ID()})
}
func hash(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func cookieHash(r *http.Request) string {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return hash(cookie.Value)
}
func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.config.Origin, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.config.Demo {
		failure(w, 404, "NOT_FOUND")
		return
	}
	var input struct {
		UserID string `json:"user_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	profile, err := s.identities.Profile(r.Context(), input.UserID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			failure(w, 401, "UNAUTHENTICATED")
		} else {
			failure(w, 503, "STORAGE_UNAVAILABLE")
		}
		return
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes())
	if err := s.identities.ReplaceSession(r.Context(), cookieHash(r), hash(token), profile.ID, s.config.Clock.Now().Add(s.config.SessionTTL)); err != nil {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	s.setCookie(w, token, int(s.config.SessionTTL/time.Second))
	respond(w, 200, map[string]any{"profile": profile})
}
func randomBytes() []byte { data := make([]byte, 32); _, _ = rand.Read(data); return data }
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.identities.DeleteSession(r.Context(), cookieHash(r)); err != nil {
		failure(w, 503, "STORAGE_UNAVAILABLE")
		return
	}
	s.setCookie(w, "", -1)
	w.WriteHeader(204)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	profile, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	respond(w, 200, map[string]any{"profile": profile, "capabilities": map[string]bool{"demo": s.config.Demo, "service_actions": profile.Role == "intern" && s.config.Clock.Now().In(s.config.Location).Format(time.DateOnly) >= profile.StartDate}})
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (demo.Profile, bool) {
	p, err := s.identities.SessionProfile(r.Context(), cookieHash(r), s.config.Clock.Now())
	if err != nil {
		if errors.Is(err, authz.ErrUnauthenticated) || errors.Is(err, authz.ErrNotFound) {
			failure(w, 401, "UNAUTHENTICATED")
		} else {
			failure(w, 503, "STORAGE_UNAVAILABLE")
		}
		return demo.Profile{}, false
	}
	return p, true
}
