package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errFaultValidation = errors.New("VALIDATION_ERROR")
var errFaultService = errors.New("NOT_FOUND")

const presenterCookie = "buddy_presenter"

func (s *Server) panelNow() time.Time {
	if s.Config.OperationalClock != nil {
		return s.Config.OperationalClock.Now()
	}
	return time.Now().UTC()
}
func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Server) presenterCookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: presenterCookie, Value: value, Path: "/demo", HttpOnly: true, Secure: strings.HasPrefix(s.Config.PanelOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) panelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /demo", s.panel)
	mux.HandleFunc("POST /demo/session", s.panelLogin)
	mux.HandleFunc("DELETE /demo/session", s.panelLogout)
	mux.HandleFunc("PATCH /demo/access-requests/{id}/status", s.panelStatus)
	mux.HandleFunc("PUT /demo/faults/{service}", s.panelFault)
	mux.HandleFunc("POST /demo/panel/status", s.panelStatusForm)
	mux.HandleFunc("POST /demo/panel/fault", s.panelFaultForm)
}
func (s *Server) panelOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != s.Config.PanelOrigin || s.Config.PanelOrigin == "" {
		failure(w, 403, "ORIGIN_NOT_ALLOWED")
		return false
	}
	return true
}
func (s *Server) presenter(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" && !s.panelOrigin(w, r) {
		return false
	}
	cookie, err := r.Cookie(presenterCookie)
	if err != nil {
		failure(w, 401, "UNAUTHENTICATED")
		return false
	}
	var active bool
	err = s.Store.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM presenter_sessions WHERE token_hash=$1 AND expires_at>$2)`, tokenHash(cookie.Value), s.panelNow()).Scan(&active)
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return false
	}
	if !active {
		failure(w, 401, "UNAUTHENTICATED")
		return false
	}
	return true
}
func (s *Server) panelLogin(w http.ResponseWriter, r *http.Request) {
	if !s.panelOrigin(w, r) {
		return
	}
	var input struct {
		Secret string `json:"secret"`
	}
	form := strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	if form {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil || len(r.PostForm) != 1 {
			failure(w, 400, "VALIDATION_ERROR")
			return
		}
		input.Secret = r.PostForm.Get("secret")
	} else if !decode(w, r, &input) {
		return
	}
	expected := sha256.Sum256([]byte(s.Config.PanelSecret))
	actual := sha256.Sum256([]byte(input.Secret))
	if len(s.Config.PanelSecret) < 32 || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		failure(w, 401, "UNAUTHENTICATED")
		return
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	defer tx.Rollback(context.WithoutCancel(r.Context()))
	if previous, err := r.Cookie(presenterCookie); err == nil {
		if _, err = tx.Exec(r.Context(), `DELETE FROM presenter_sessions WHERE token_hash=$1`, tokenHash(previous.Value)); err != nil {
			failure(w, 503, "SERVICE_UNAVAILABLE")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO presenter_sessions(token_hash,expires_at) VALUES($1,$2)`, tokenHash(token), s.panelNow().Add(time.Hour)); err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	if tx.Commit(r.Context()) != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	s.presenterCookie(w, token, 3600)
	if form {
		http.Redirect(w, r, "/demo", http.StatusSeeOther)
	} else {
		response(w, 200, map[string]bool{"presenter": true})
	}
}
func (s *Server) panelLogout(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	cookie, _ := r.Cookie(presenterCookie)
	if _, err := s.Store.Pool.Exec(r.Context(), `DELETE FROM presenter_sessions WHERE token_hash=$1`, tokenHash(cookie.Value)); err != nil {
		failure(w, 503, "SERVICE_UNAVAILABLE")
		return
	}
	s.presenterCookie(w, "", -1)
	w.WriteHeader(204)
}

type statusInput struct {
	Version int64  `json:"expected_version"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Server) panelStatus(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	var input statusInput
	if !decode(w, r, &input) {
		return
	}
	result, err := s.AccessStatus(r.Context(), r.PathValue("id"), input.Version, input.Status, input.Reason)
	if err != nil {
		failure(w, 500, "SERVICE_ERROR")
		return
	}
	rawResponse(w, result.Status, result.Body)
}

type faultInput struct {
	Mode  string `json:"mode"`
	Delay int    `json:"delay_ms"`
}

func (s *Server) setFault(ctx context.Context, service string, input faultInput) error {
	if service != "access" && service != "time_off" {
		return errFaultService
	}
	switch input.Mode {
	case "normal", "unavailable_before", "delay_before", "lose_response_after", "reconciliation_unavailable":
	default:
		return errFaultValidation
	}
	if input.Delay < 0 || input.Delay > 10000 {
		return errFaultValidation
	}
	_, err := s.Store.Pool.Exec(ctx, `UPDATE faults SET mode=$2,delay_ms=$3 WHERE service=$1`, service, input.Mode, input.Delay)
	return err
}
func (s *Server) panelFault(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	var input faultInput
	if !decode(w, r, &input) {
		return
	}
	service := r.PathValue("service")
	if service != "access" && service != "time_off" {
		failure(w, 404, "NOT_FOUND")
		return
	}
	if err := s.setFault(r.Context(), service, input); err != nil {
		if errors.Is(err, errFaultValidation) {
			failure(w, 400, "VALIDATION_ERROR")
		} else {
			failure(w, 503, "SERVICE_UNAVAILABLE")
		}
		return
	}
	response(w, 200, map[string]bool{"saved": true})
}
func (s *Server) panel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := r.Cookie(presenterCookie); err != nil {
		_ = panelTemplate.Execute(w, nil)
		return
	}
	if !s.presenter(w, r) {
		return
	}
	rows, err := s.Store.Pool.Query(r.Context(), `SELECT `+accessColumns+` FROM access_requests ORDER BY created_at DESC,id DESC`)
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
	_ = panelTemplate.Execute(w, struct{ Items []AccessRequest }{items})
}
func (s *Server) panelStatusForm(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	result, err := s.AccessStatus(r.Context(), r.PostForm.Get("id"), version, r.PostForm.Get("status"), r.PostForm.Get("reason"))
	if err != nil {
		failure(w, 500, "SERVICE_ERROR")
		return
	}
	if result.Status != 200 {
		rawResponse(w, result.Status, result.Body)
		return
	}
	http.Redirect(w, r, "/demo", http.StatusSeeOther)
}
func (s *Server) panelFaultForm(w http.ResponseWriter, r *http.Request) {
	if !s.presenter(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	delay, err := strconv.Atoi(r.PostForm.Get("delay_ms"))
	if err != nil {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	if err := s.setFault(r.Context(), r.PostForm.Get("service"), faultInput{r.PostForm.Get("mode"), delay}); err != nil {
		failure(w, 400, "VALIDATION_ERROR")
		return
	}
	http.Redirect(w, r, "/demo", http.StatusSeeOther)
}

var panelTemplate = template.Must(template.New("panel").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><title>Ведущий — AI-buddy</title><style>body{font:16px system-ui;max-width:1100px;margin:40px auto;padding:20px;background:#f5f7fb;color:#202333}article,form{padding:16px;background:white;border-radius:12px;margin:12px 0}input,select,button{padding:10px;margin:4px}button{cursor:pointer}small{color:#596579}</style><h1>Панель ведущего — демонстрация</h1><p>Все сотрудники, регламенты и сервисы синтетические. Панель не выполняет реальные корпоративные операции.</p>{{if .}}<h2>Заявки на доступ</h2>{{range .Items}}<article><strong>{{.ID}}</strong> · {{.UserID}} · {{.ResourceID}} · {{.Status}} · версия {{.Version}}{{if .Reason}}<p>{{.Reason}}</p>{{end}}<form method="post" action="/demo/panel/status"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="version" value="{{.Version}}"><select name="status"><option value="in_review">На рассмотрении</option><option value="approved">Одобрить и выдать доступ</option><option value="rejected">Отклонить</option><option value="cancelled">Отменить</option></select><input name="reason" maxlength="500" placeholder="Демонстрационная причина отказа"><button>Изменить статус</button></form></article>{{end}}<h2>Сбои сервиса</h2><form method="post" action="/demo/panel/fault"><select name="service"><option value="access">Доступ</option><option value="time_off">Отгулы</option></select><select name="mode"><option value="normal">Обычный режим</option><option value="unavailable_before">Недоступен до обработки</option><option value="delay_before">Задержка перед обработкой</option><option value="lose_response_after">Потерять ответ после обработки</option><option value="reconciliation_unavailable">Сверка недоступна</option></select><input name="delay_ms" type="number" min="0" max="10000" value="0" aria-label="Задержка, мс"><button>Установить режим</button></form><h2>Сброс</h2><p>Полный сброс будет доступен командой <code>make demo-reset CONFIRM=demo</code> после реализации задачи 7.6. Выборочный сброс имитаций при работающем worker запрещён.</p>{{else}}<form method="post" action="/demo/session"><label>Секрет ведущего <input type="password" name="secret" required autocomplete="off"></label><button>Войти ведущим</button></form><small>Секрет задаётся переменной DEMO_ADMIN_SECRET. Пользовательская сессия и SERVICE_SECRET не подходят.</small>{{end}}</html>`))
