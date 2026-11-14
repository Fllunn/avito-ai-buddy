package api

import (
	"ai-buddy/backend/internal/agent"
	"encoding/json"
	"html/template"
	"net/http"
)

var documentTemplate = template.Must(template.New("document").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><title>{{.Title}}</title><style>body{font:18px system-ui;max-width:760px;margin:60px auto;padding:24px;line-height:1.7}small{color:#687583}</style><h1>{{.Title}}</h1><small>Демонстрационный материал · владелец {{.Owner}}{{if .Stale}} · УСТАРЕЛ{{end}}</small>{{range .Fragments}}<p>{{.Text}}</p>{{else}}<p>Нет проверенных фрагментов для показа. Уточни актуальный порядок у владельца.</p>{{end}}</html>`))

func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	frame, e := s.config.Business.localFrame(r.Context(), p.ID)
	if e != nil {
		failure(w, 503, "CONTEXT_UNAVAILABLE")
		return
	}
	for _, d := range frame.Documents {
		if d.ID != r.PathValue("id") {
			continue
		}
		var fragments []agent.Fragment
		if json.Unmarshal(d.Fragments, &fragments) != nil {
			failure(w, 503, "STORAGE_UNAVAILABLE")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = documentTemplate.Execute(w, struct {
			Title, Owner string
			Stale        bool
			Fragments    []agent.Fragment
		}{d.Title, d.OwnerID, d.Stale, fragments})
		return
	}
	failure(w, 404, "NOT_FOUND")
}
