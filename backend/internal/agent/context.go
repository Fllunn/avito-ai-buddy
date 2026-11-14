package agent

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"errors"
)

type Context struct {
	Profile   demo.Profile      `json:"profile"`
	Steps     []demo.Step       `json:"steps"`
	Documents []demo.Document   `json:"documents"`
	Resources []demo.Resource   `json:"resources"`
	History   []actions.Message `json:"history"`
}
type Source interface {
	Profile(context.Context, string) (demo.Profile, error)
	Steps(context.Context, string) ([]demo.Step, error)
	Documents(context.Context, demo.Profile) ([]demo.Document, error)
	Messages(context.Context, string, int64) ([]actions.Message, error)
}
type Directory interface {
	Resources(context.Context, string) ([]demo.Resource, error)
}
type Builder struct {
	Source    Source
	Directory Directory
}

var ErrContext = errors.New("CONTEXT_UNAVAILABLE")

func (b Builder) Build(ctx context.Context, user string) (Context, error) {
	p, err := b.Source.Profile(ctx, user)
	if err != nil || !p.Enabled || p.ID != user {
		return Context{}, ErrContext
	}
	steps, err := b.Source.Steps(ctx, user)
	if err != nil {
		return Context{}, ErrContext
	}
	documents, err := b.Source.Documents(ctx, p)
	if err != nil {
		return Context{}, ErrContext
	}
	resources, err := b.Directory.Resources(ctx, user)
	if err != nil {
		return Context{}, ErrContext
	}
	history, err := b.Source.Messages(ctx, user, 0)
	if err != nil {
		return Context{}, ErrContext
	}
	result := Context{Profile: p, Steps: authz.Steps(p, steps), Documents: []demo.Document{}, Resources: []demo.Resource{}, History: []actions.Message{}}
	for _, d := range documents {
		if authz.Document(p, d) {
			result.Documents = append(result.Documents, safeDocument(d))
		}
	}
	for _, r := range resources {
		if authz.Resource(p, r) == authz.ErrNotFound {
			continue
		}
		if r.Kind == "closed" {
			r = demo.Resource{ID: r.ID, Name: r.Name, Kind: r.Kind, Levels: []string{}, RecommendedTeams: []string{}, Synonyms: []string{}}
		}
		result.Resources = append(result.Resources, r)
	}
	for _, m := range AuthorizedHistory(result, history) {
		if m.UserID != user {
			continue
		}
		// Defense in depth for older persisted history: only cleaned text and slots
		// enter the provider, even when a source returns an outdated record.
		if !actions.KnownIntent(m.Intent) {
			m.Intent = ""
		}
		m.Metadata = nil
		m.Text = safety.Prepare(ctx, safety.Local{}, m.Text).String()
		copied := map[string]string{}
		for key, value := range m.Slots {
			switch key {
			case "resource_id", "level", "justification", "start_date", "end_date", "request_id", "step_id", "query":
			default:
				continue
			}
			copied[key] = safety.Prepare(ctx, safety.Local{}, value).String()
		}
		m.Slots = copied
		result.History = append(result.History, m)
	}
	current, err := b.Source.Profile(ctx, user)
	if err != nil || !authz.SameScope(p, current) {
		return Context{}, ErrContext
	}
	return result, nil
}

func (b Builder) Current(ctx context.Context, frame Context) error {
	current, err := b.Source.Profile(ctx, frame.Profile.ID)
	if err != nil || !authz.SameScope(frame.Profile, current) {
		return ErrContext
	}
	return nil
}
