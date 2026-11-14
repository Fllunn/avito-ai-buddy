package agent

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/safety"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
)

type Slots struct {
	ResourceID    string `json:"resource_id,omitempty"`
	Level         string `json:"level,omitempty"`
	Justification string `json:"justification,omitempty"`
	StartDate     string `json:"start_date,omitempty"`
	EndDate       string `json:"end_date,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	StepID        string `json:"step_id,omitempty"`
	Query         string `json:"query,omitempty"`
}
type Intent struct {
	Name       string  `json:"name"`
	Slots      Slots   `json:"slots"`
	Confidence float64 `json:"confidence"`
}
type Intents struct {
	Intents     []Intent `json:"intents"`
	Ambiguities []string `json:"ambiguities"`
}
type FragmentSelection struct {
	DocumentID string `json:"document_id"`
	FragmentID string `json:"fragment_id"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}
type Provider interface {
	Recognize(context.Context, Context, string) ([]byte, error)
	SelectFragments(context.Context, Context, string) ([]byte, error)
	Mode() string
}
type Recognition struct {
	Data     Intents
	Fallback bool
	Code     string
	Mode     string
}
type Recognizer struct {
	Builder  Builder
	Provider Provider
	Timeout  time.Duration
}

func (r Recognizer) Recognize(ctx context.Context, user string, query safety.Text) (result Recognition) {
	result = Recognition{Mode: r.Provider.Mode()}
	if query.UseForm() {
		return fallback(result, "USE_FORM")
	}
	callCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	frame, err := r.Builder.Build(callCtx, user)
	if err != nil {
		return fallback(result, "CONTEXT_UNAVAILABLE")
	}
	defer func() {
		check, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if r.Builder.Current(check, frame) != nil {
			result = fallback(result, "CONTEXT_UNAVAILABLE")
		}
	}()
	type answer struct {
		data []byte
		err  error
	}
	done := make(chan answer, 1)
	go func() { data, err := r.Provider.Recognize(callCtx, frame, query.String()); done <- answer{data, err} }()
	select {
	case <-callCtx.Done():
		return fallback(result, "LLM_TIMEOUT")
	case reply := <-done:
		if reply.err != nil || callCtx.Err() != nil {
			return fallback(result, "LLM_UNAVAILABLE")
		}
		if !decode(reply.data, &result.Data) || len(result.Data.Intents) == 0 || result.Data.Ambiguities == nil {
			return fallback(result, "INVALID_LLM_RESULT")
		}
		for i := range result.Data.Intents {
			intent := &result.Data.Intents[i]
			if !actions.KnownIntent(intent.Name) || intent.Confidence < 0 || intent.Confidence > 1 {
				return fallback(result, "INVALID_LLM_RESULT")
			}
			if intent.Confidence < 0.8 || len(result.Data.Ambiguities) > 0 {
				result.Code = "CLARIFY"
			}
			for _, field := range []*string{&intent.Slots.ResourceID, &intent.Slots.Level, &intent.Slots.Justification, &intent.Slots.StartDate, &intent.Slots.EndDate, &intent.Slots.RequestID, &intent.Slots.StepID, &intent.Slots.Query} {
				cleaned := safety.Prepare(callCtx, safety.Local{}, *field)
				if cleaned.UseForm() {
					return fallback(result, "USE_FORM")
				}
				*field = cleaned.String()
			}

		}
		for i := range result.Data.Ambiguities {
			result.Data.Ambiguities[i] = "needs_clarification"
		}
		return result
	}
}
func fallback(result Recognition, code string) Recognition {
	result.Fallback = true
	result.Code = code
	result.Data = Intents{Intents: []Intent{}, Ambiguities: []string{}}
	return result
}

func decode(data []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return len(data) <= 65536 && decoder.Decode(target) == nil && decoder.Decode(new(any)) == io.EOF
}

// Fake is a deterministic demonstration provider, never a real language model.
type Fake struct{}

func (Fake) Mode() string { return "fake" }
func (Fake) Recognize(ctx context.Context, _ Context, query string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	accessAt, absenceAt := strings.Index(q, "доступ"), strings.Index(q, "отгул")
	if accessAt >= 0 && absenceAt >= 0 {
		first, second := "access_request", "time_off_request"
		split := absenceAt
		if absenceAt < accessAt {
			first, second = "time_off_request", "access_request"
			split = accessAt
		}
		return json.Marshal(Intents{Intents: []Intent{{Name: first, Slots: Slots{Query: query[:split]}, Confidence: 1}, {Name: second, Slots: Slots{Query: query[split:]}, Confidence: 1}}, Ambiguities: []string{}})
	}
	name := "unclear"
	confidence := 0.5
	switch {
	case strings.Contains(q, "командиров") || strings.Contains(q, "больнич") || strings.Contains(q, "напиши код"):
		name = "out_of_scope"
	case strings.Contains(q, "отгул") && (strings.Contains(q, "как") || strings.Contains(q, "инструкц") || strings.Contains(q, "правил")):
		name = "time_off_info"
	case strings.Contains(q, "отгул"):
		name = "time_off_request"
	case strings.Contains(q, "доступ") && (strings.Contains(q, "как") || strings.Contains(q, "инструкц")):
		name = "find_doc"
	case strings.Contains(q, "доступ") || strings.Contains(q, "не могу открыть") || strings.Contains(q, "нет прав"):
		name = "access_request"
	case strings.Contains(q, "отмени"):
		name = "cancel"
	case strings.Contains(q, "согласовали") || strings.Contains(q, "что с моей заявкой"):
		name = "status"
	case strings.Contains(q, "поменяй") || strings.Contains(q, "вместо") || strings.HasPrefix(q, "а нет") || strings.Contains(q, "лучше ") || strings.Contains(q, "давай редактирование"):
		name = "card_edit"
	case strings.Contains(q, "познакомилась") || strings.Contains(q, "познакомился"):
		name = "mark_done"
	case strings.Contains(q, "с чего начать") || strings.Contains(q, "что дальше") || strings.Contains(q, "что мне делать"):
		name = "next_step"
	case strings.Contains(q, "инструкц") || strings.Contains(q, "spt") || strings.Contains(q, "пишут код"):
		name = "find_doc"
	case strings.Contains(q, "наставник") || strings.Contains(q, "куратор") || strings.Contains(q, "программ") || strings.Contains(q, "чаты") || strings.Contains(q, "кому писать") || strings.Contains(q, "цели"):
		name = "who_to_ask"
	case q == "да" || q == "да, отправляй":
		name = "card_confirm"
	case q == "не надо" || q == "отмена":
		name = "card_cancel"
	case strings.Contains(q, "привет") || strings.Contains(q, "спасибо"):
		name = "smalltalk"
	}
	if name != "unclear" {
		confidence = 1
	}
	slots := Slots{Query: query}
	if name == "access_request" {
		if strings.Contains(q, "админ") {
			slots.Level = "admin"
		} else if strings.Contains(q, "редактирован") {
			slots.Level = "edit"
		} else if strings.Contains(q, "запис") {
			slots.Level = "write"
		} else if strings.Contains(q, "чтени") {
			slots.Level = "read"
		}
	}
	return json.Marshal(Intents{Intents: []Intent{{Name: name, Slots: slots, Confidence: confidence}}, Ambiguities: []string{}})
}
func (Fake) SelectFragments(ctx context.Context, frame Context, _ string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, d := range frame.Documents {
		var fragments []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if d.Stale || json.Unmarshal(d.Fragments, &fragments) != nil || len(fragments) == 0 {
			continue
		}
		return json.Marshal(struct {
			Selections []FragmentSelection `json:"selections"`
		}{[]FragmentSelection{{d.ID, fragments[0].ID, 0, len([]rune(fragments[0].Text))}}})
	}
	return []byte(`{"selections":[]}`), nil
}
