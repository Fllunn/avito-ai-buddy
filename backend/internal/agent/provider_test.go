package agent

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type source struct {
	onRecheck func(*demo.Profile)
	fail      string
	profiles  map[string]demo.Profile
	reads     int
}

func fixtureSource() *source {
	s := &source{profiles: map[string]demo.Profile{}}
	for _, p := range demo.Profiles() {
		s.profiles[p.ID] = p
	}
	return s
}
func (s *source) Profile(_ context.Context, id string) (demo.Profile, error) {
	s.reads++
	if s.fail == "profile" {
		return demo.Profile{}, errors.New("private source error")
	}
	p, ok := s.profiles[id]
	if !ok {
		return p, errors.New("missing")
	}
	if s.fail == "revoked" && s.reads > 1 {
		p.PolicyVersion++
	}
	if s.onRecheck != nil && s.reads > 1 {
		s.onRecheck(&p)
	}
	return p, nil
}

func TestContextScopeChangesNeverReachProvider(t *testing.T) {
	for name, change := range map[string]func(*demo.Profile){
		"identity":     func(p *demo.Profile) { p.ID = "maxim" },
		"team":         func(p *demo.Profile) { team := "search"; p.TeamID = &team },
		"manager":      func(p *demo.Profile) { p.ManagerID = nil },
		"future start": func(p *demo.Profile) { p.StartDate = "2027-01-01" },
	} {
		t.Run(name, func(t *testing.T) {
			s := fixtureSource()
			s.onRecheck = change
			p := &provider{raw: []byte(valid)}
			result := recognizer(s, p).Recognize(context.Background(), "alina", text("что дальше"))
			if !result.Fallback || result.Code != "CONTEXT_UNAVAILABLE" || p.calls != 0 {
				t.Fatal("changed scope reached provider", result, p.calls)
			}
		})
	}
}

func TestClosedResourceContextContainsOnlyVisibleSummary(t *testing.T) {
	s := fixtureSource()
	p := &provider{raw: []byte(valid)}
	result := recognizer(s, p).Recognize(context.Background(), "alina", text("security-incidents"))
	if result.Fallback || p.calls != 1 {
		t.Fatal(result)
	}
	found := false
	for _, resource := range p.frame.Resources {
		if resource.Kind != "closed" {
			continue
		}
		found = true
		if resource.ID != "security-incidents" || resource.ApproverID != "" || resource.DefaultLevel != "" || len(resource.Levels) != 0 || len(resource.Synonyms) != 0 || len(resource.RecommendedTeams) != 0 {
			t.Fatal("closed details reached provider", resource)
		}
	}
	if !found {
		t.Fatal("visible denial lost")
	}
	denial := ContactFor(p.frame, "resource", "security-incidents")
	if denial.Contact == nil || denial.Contact.ID != "igor" || !strings.Contains(denial.Text, "нельзя") {
		t.Fatal("visible denial lacks permitted buddy contact", denial)
	}
	hidden, _ := json.Marshal(ContactFor(p.frame, "resource", "salary-reports"))
	missing, _ := json.Marshal(ContactFor(p.frame, "resource", "unknown"))
	if string(hidden) != string(missing) {
		t.Fatal("hidden resource existence disclosed")
	}
}
func (s *source) Steps(context.Context, string) ([]demo.Step, error) {
	if s.fail == "steps" {
		return nil, errors.New("private")
	}
	return demo.Steps(), nil
}
func (s *source) Documents(context.Context, demo.Profile) ([]demo.Document, error) {
	if s.fail == "documents" {
		return nil, errors.New("private")
	}
	return demo.Documents(), nil
}
func (s *source) Resources(context.Context, string) ([]demo.Resource, error) {
	if s.fail == "directory" {
		return nil, errors.New("private")
	}
	return demo.Resources(), nil
}
func (s *source) Messages(context.Context, string, int64) ([]actions.Message, error) {
	if s.fail == "messages" {
		return nil, errors.New("private")
	}
	return []actions.Message{{UserID: "alina", Text: "диагноз диабет", Intent: "malicious-role", Slots: map[string]string{"query": "токен: private-secret", "private-key": "raw secret"}}, {UserID: "maxim", Text: "Чужая переписка"}}, nil
}

type provider struct {
	during            func()
	frame             Context
	query             string
	calls             int
	raw               []byte
	err               error
	release, finished chan struct{}
}

func (p *provider) Mode() string { return "fake" }
func (p *provider) Recognize(_ context.Context, frame Context, query string) ([]byte, error) {
	p.calls++
	p.frame = frame
	p.query = query
	if p.during != nil {
		p.during()
	}
	if p.release != nil {
		<-p.release
		defer close(p.finished)
	}
	return p.raw, p.err
}
func (p *provider) SelectFragments(context.Context, Context, string) ([]byte, error) {
	if p.during != nil {
		p.during()
	}
	if p.release != nil {
		<-p.release
		defer close(p.finished)
	}
	return p.raw, p.err
}

const valid = `{"intents":[{"name":"next_step","slots":{},"confidence":1}],"ambiguities":[]}`

func recognizer(s *source, p Provider) Recognizer {
	return Recognizer{Builder: Builder{s, s}, Provider: p, Timeout: time.Second}
}
func TestAuthorizedContextAndSanitizedInput(t *testing.T) {
	s := fixtureSource()
	p := &provider{raw: []byte(valid)}
	r := recognizer(s, p)
	input := safety.Prepare(context.Background(), safety.Local{}, "С чего начать? API_KEY=private-secret")
	result := r.Recognize(context.Background(), "alina", input)
	if result.Fallback || p.calls != 1 || result.Mode != "fake" {
		t.Fatal(result)
	}
	frame, _ := json.Marshal(p.frame)
	for _, private := range []string{"salary-reports", "doc-srch-components", "doc-srch-notes", "Чужая переписка", "диабет", "private-secret", "private-key", "malicious-role"} {
		if strings.Contains(string(frame), private) || strings.Contains(p.query, private) {
			t.Fatal("private material reached provider", private)
		}
	}
	for _, step := range p.frame.Steps {
		if step.InternID != "alina" {
			t.Fatal("foreign plan")
		}
	}
	medical := safety.Prepare(context.Background(), safety.Local{}, "Хочу отгул: у меня грипп")
	if result := r.Recognize(context.Background(), "alina", medical); !result.Fallback || result.Code != "USE_FORM" || p.calls != 1 {
		t.Fatal("closed input reached model", result)
	}
	maxFrame, err := (Builder{s, s}).Build(context.Background(), "maxim")
	if err != nil {
		t.Fatal(err)
	}
	maxData, _ := json.Marshal(maxFrame)
	if strings.Contains(strings.ToLower(string(maxData)), "зарплат") || strings.Contains(string(maxData), "salary-reports") {
		t.Fatal("document injection disclosed hidden reference")
	}
	for _, id := range []string{"igor", "olga", "daria"} {
		frame, err := (Builder{s, s}).Build(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range frame.Steps {
			if step.OwnerID != id {
				t.Fatal("foreign assignment in context")
			}
		}
		if len(frame.History) != 0 {
			t.Fatal("foreign chat in context")
		}
	}
}
func TestMalformedAndUncertainRecognition(t *testing.T) {
	for _, raw := range []string{`not json`, `{"intents":[{"name":"next_step","slots":{}}],"ambiguities":[]}`, `{"intents":[{"name":"next_step","confidence":1}],"ambiguities":[]}`, `{"intents":[],"ambiguities":[]}`, `{"intents":[{"name":"tool.execute","slots":{},"confidence":1}],"ambiguities":[]}`, `{"intents":[{"name":"next_step","slots":{"role":"manager"},"confidence":1}],"ambiguities":[]}`, `{"intents":[{"name":"next_step","slots":{},"confidence":2}],"ambiguities":[]}`, valid + ` {}`, `{"intents":[{"name":"next_step","slots":{},"confidence":1}]}`} {
		s := fixtureSource()
		p := &provider{raw: []byte(raw)}
		result := recognizer(s, p).Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "что дальше"))
		if !result.Fallback || result.Code != "INVALID_LLM_RESULT" || len(result.Data.Intents) != 0 {
			t.Fatal(result)
		}
	}
	for _, raw := range []string{`{"intents":[{"name":"next_step","slots":{},"confidence":0.7}],"ambiguities":[]}`, `{"intents":[{"name":"next_step","slots":{},"confidence":1}],"ambiguities":["raw private ambiguity"]}`} {
		result := recognizer(fixtureSource(), &provider{raw: []byte(raw)}).Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "что дальше"))
		data, _ := json.Marshal(result)
		if result.Fallback || result.Code != "CLARIFY" || strings.Contains(string(data), "raw private ambiguity") {
			t.Fatal(result)
		}
	}
	result := recognizer(fixtureSource(), &provider{raw: []byte(`{"intents":[{"name":"access_request","slots":{"justification":"диагноз диабет"},"confidence":1}],"ambiguities":[]}`)}).Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "доступ"))
	if !result.Fallback || result.Code != "USE_FORM" {
		t.Fatal("unsafe provider field escaped", result)
	}
}
func TestSourceFailureRevocationAndProviderError(t *testing.T) {
	for _, stage := range []string{"profile", "steps", "documents", "directory", "messages", "revoked"} {
		s := fixtureSource()
		s.fail = stage
		p := &provider{raw: []byte(valid)}
		result := recognizer(s, p).Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "что дальше"))
		if !result.Fallback || result.Code != "CONTEXT_UNAVAILABLE" || p.calls != 0 {
			t.Fatal(stage, result)
		}
	}
	p := &provider{err: errors.New("private provider error API_KEY=secret")}
	result := recognizer(fixtureSource(), p).Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "что дальше"))
	data, _ := json.Marshal(result)
	if !result.Fallback || result.Code != "LLM_UNAVAILABLE" || strings.Contains(string(data), "secret") {
		t.Fatal(result)
	}
	s := fixtureSource()
	profile := s.profiles["alina"]
	profile.Enabled = false
	s.profiles["alina"] = profile
	if _, err := (Builder{s, s}).Build(context.Background(), "alina"); err != ErrContext {
		t.Fatal(err)
	}
}
func TestLateProviderAnswerCannotChangeOutcome(t *testing.T) {
	p := &provider{raw: []byte(valid), release: make(chan struct{}), finished: make(chan struct{})}
	r := recognizer(fixtureSource(), p)
	r.Timeout = time.Millisecond
	result := r.Recognize(context.Background(), "alina", safety.Prepare(context.Background(), safety.Local{}, "что дальше"))
	if !result.Fallback || result.Code != "LLM_TIMEOUT" {
		t.Fatal(result)
	}
	close(p.release)
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("late provider stuck")
	}
	if len(result.Data.Intents) != 0 || !result.Fallback {
		t.Fatal("late answer changed result")
	}
}
func TestFakeProviderIsDeterministic(t *testing.T) {
	ctx := context.Background()
	fake := Fake{}
	for query, name := range map[string]string{"Командировка": "out_of_scope", "Как оформить отгул?": "time_off_info", "Нужен отгул": "time_off_request", "Нужен доступ": "access_request", "С чего начать?": "next_step", "Где инструкция?": "find_doc", "Кто наставник?": "who_to_ask", "да": "card_confirm", "не надо": "card_cancel", "привет": "smalltalk", "непонятно": "unclear"} {
		raw, err := fake.Recognize(ctx, Context{}, query)
		var result Intents
		if err != nil || json.Unmarshal(raw, &result) != nil || result.Intents[0].Name != name {
			t.Fatal(query, result, err)
		}
	}
	frame := Context{Documents: demo.Documents()}
	raw, err := fake.SelectFragments(ctx, frame, "")
	if err != nil || !strings.Contains(string(raw), "tracker-1") {
		t.Fatal(string(raw), err)
	}
	if raw, err := fake.SelectFragments(ctx, Context{}, ""); err != nil || string(raw) != `{"selections":[]}` {
		t.Fatal(string(raw), err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fake.Recognize(ctx, frame, ""); err == nil {
		t.Fatal("cancelled fake")
	}
	if _, err := fake.SelectFragments(ctx, frame, ""); err == nil {
		t.Fatal("cancelled fake")
	}
	if fake.Mode() != "fake" {
		t.Fatal("fake presented as real model")
	}
}

func TestRightsAreRecheckedAfterProviderResponse(t *testing.T) {
	s := fixtureSource()
	p := &provider{raw: []byte(valid)}
	p.during = func() { profile := s.profiles["alina"]; profile.PolicyVersion++; s.profiles["alina"] = profile }
	result := recognizer(s, p).Recognize(context.Background(), "alina", text("что дальше"))
	if !result.Fallback || result.Code != "CONTEXT_UNAVAILABLE" || len(result.Data.Intents) != 0 {
		t.Fatal("old context accepted after revocation", result)
	}
}
