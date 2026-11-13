package agent

import (
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

type SourceLink struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Stale   bool   `json:"stale"`
	Demo    bool   `json:"demo"`
	OwnerID string `json:"owner_id"`
}
type Answer struct {
	Text      string       `json:"text"`
	Sources   []SourceLink `json:"sources"`
	Code      string       `json:"code"`
	Mode      string       `json:"mode"`
	ContactID *string      `json:"contact_id,omitempty"`
}
type Answerer struct {
	Search   Search
	Provider Provider
	Timeout  time.Duration
}

func (a Answerer) Answer(ctx context.Context, user string, query safety.Text) (answer Answer) {
	answer = Answer{Sources: []SourceLink{}, Mode: a.Provider.Mode()}
	callCtx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	if query.UseForm() {
		answer.Code = "USE_FORM"
		answer.Text = "Продолжи через форму без чувствительных сведений."
		return answer
	}
	frame, err := a.Search.Find(callCtx, user, query)
	if err != nil {
		answer.Code = "SEARCH_UNAVAILABLE"
		answer.Text = "Не удалось проверить источники. Попробуй ещё раз."
		return answer
	}
	defer func() {
		check, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if a.Search.Builder.Current(check, frame) != nil {
			answer = Answer{Sources: []SourceLink{}, Mode: a.Provider.Mode(), Code: "CONTEXT_UNAVAILABLE", Text: "Не удалось подтвердить права на источники. Попробуй ещё раз."}
		}
	}()
	answer.ContactID = frame.Profile.CuratorID
	if frame.Profile.BuddyID != nil {
		answer.ContactID = frame.Profile.BuddyID
	}
	current := false
	for _, d := range frame.Documents {
		if validLink(d.URL) {
			answer.Sources = append(answer.Sources, SourceLink{d.ID, d.Title, d.URL, d.Stale, d.Demo, d.OwnerID})
		}
		if !d.Stale {
			current = true
		}
	}
	if len(answer.Sources) == 0 {
		answer.Code = "NO_SOURCE"
		answer.Text = "Не нашёл доступной инструкции. Уточни у ответственного."
		return answer
	}
	if !current {
		answer.Code = "STALE_ONLY"
		answer.Text = "Найден только устаревший демонстрационный материал. Уточни актуальный порядок у владельца."
		return answer
	}
	for range 2 {
		if a.Search.Builder.Current(callCtx, frame) != nil {
			answer.Code = "CONTEXT_UNAVAILABLE"
			return answer
		}
		type response struct {
			data []byte
			err  error
		}
		done := make(chan response, 1)
		go func() {
			data, err := a.Provider.SelectFragments(callCtx, frame, query.String())
			done <- response{data, err}
		}()
		select {
		case <-callCtx.Done():
			answer.Code = "LLM_TIMEOUT"
			answer.Text = "Ответ не готов. Открой найденные источники."
			return answer
		case reply := <-done:
			if reply.err != nil || callCtx.Err() != nil {
				answer.Code = "LLM_UNAVAILABLE"
				answer.Text = "Модель недоступна. Открой найденные источники."
				return answer
			}
			if text, ok := grounded(frame, reply.data); ok {
				answer.Code = "GROUNDED"
				answer.Text = text
				return answer
			}
		}
	}
	answer.Code = "LINKS_ONLY"
	answer.Text = "Не удалось подтвердить ответ фрагментами. Открой найденные источники."
	return answer
}
func validLink(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" || parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(parsed.Path, "/demo/docs/")
}
func grounded(frame Context, raw []byte) (string, bool) {
	var selected struct {
		Selections []FragmentSelection `json:"selections"`
	}
	if !decode(raw, &selected) || len(selected.Selections) == 0 || len(selected.Selections) > 3 {
		return "", false
	}
	excerpts := []string{}
	for _, choice := range selected.Selections {
		var document *demo.Document
		for i := range frame.Documents {
			if frame.Documents[i].ID == choice.DocumentID {
				document = &frame.Documents[i]
				break
			}
		}
		if document == nil || document.Stale || !validLink(document.URL) {
			return "", false
		}
		var fragments []Fragment
		if json.Unmarshal(document.Fragments, &fragments) != nil {
			return "", false
		}
		found := false
		for _, f := range fragments {
			if f.ID == choice.FragmentID {
				text := []rune(f.Text)
				if choice.Start < 0 || choice.End <= choice.Start || choice.End > len(text) {
					return "", false
				}
				label := ""
				if document.Demo {
					label = " (демонстрационный материал)"
				}
				excerpts = append(excerpts, document.Title+label+": «"+string(text[choice.Start:choice.End])+"»")
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return strings.Join(excerpts, "\n\n"), true
}
