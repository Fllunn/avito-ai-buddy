package agent

import (
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func text(value string) safety.Text {
	return safety.Prepare(context.Background(), safety.Local{}, value)
}
func TestSearchRightsSynonymsAndFailure(t *testing.T) {
	s := fixtureSource()
	search := Search{Builder{s, s}}
	for _, tc := range []struct{ user, query, id string }{{"alina", "джира", "doc-tracker"}, {"alina", "как пишут код", "doc-pay-principles"}, {"maxim", "библиотека компонентов", "doc-srch-components"}, {"maxim", "spt", "doc-spt"}} {
		frame, err := search.Find(context.Background(), tc.user, text(tc.query))
		if err != nil || len(frame.Documents) == 0 || frame.Documents[0].ID != tc.id {
			t.Fatal(tc, frame.Documents, err)
		}
	}
	frame, err := search.Find(context.Background(), "maxim", text("doc-pay-principles"))
	if err != nil || len(frame.Documents) != 0 {
		t.Fatal("foreign document found", frame, err)
	}
	s.fail = "documents"
	if _, err := search.Find(context.Background(), "alina", text("трекер")); err != ErrContext {
		t.Fatal(err)
	}
}
func TestGroundedAnswerAndUnavailableSources(t *testing.T) {
	s := fixtureSource()
	a := Answerer{Search{Builder{s, s}}, Fake{}, time.Second}
	result := a.Answer(context.Background(), "alina", text("джира"))
	if result.Code != "GROUNDED" || !strings.Contains(result.Text, "Демонстрационная инструкция") || len(result.Sources) != 1 || result.ContactID == nil || *result.ContactID != "igor" {
		t.Fatal(result)
	}
	result = a.Answer(context.Background(), "alina", text("дежурства"))
	if result.Code != "STALE_ONLY" || !result.Sources[0].Stale || result.Sources[0].OwnerID != "olga" {
		t.Fatal(result)
	}
	result = a.Answer(context.Background(), "alina", text("VPN ноутбука"))
	if result.Code != "NO_SOURCE" || len(result.Sources) != 0 {
		t.Fatal(result)
	}
	result = a.Answer(context.Background(), "maxim", text("заметки команды"))
	if strings.Contains(strings.ToLower(result.Text), "зарплат") || strings.Contains(result.Text, "salary-reports") {
		t.Fatal("injection repeated", result)
	}
	result = a.Answer(context.Background(), "alina", text("диагноз диабет"))
	if result.Code != "USE_FORM" {
		t.Fatal(result)
	}
	s.fail = "documents"
	result = a.Answer(context.Background(), "alina", text("трекер"))
	if result.Code != "SEARCH_UNAVAILABLE" || len(result.Sources) != 0 {
		t.Fatal(result)
	}
}
func TestUnverifiedProviderOutputIsDiscarded(t *testing.T) {
	for _, raw := range []string{`{"text":"Доступ уже выдан","selections":[]}`, `{"selections":[{"document_id":"doc-srch-components","fragment_id":"search-1","start":0,"end":10}]}`, `{"selections":[{"document_id":"doc-tracker","fragment_id":"missing","start":0,"end":10}]}`, `{"selections":[{"document_id":"doc-tracker","fragment_id":"tracker-1","start":-1,"end":10}]}`, `{"selections":[{"document_id":"doc-tracker","fragment_id":"tracker-1","start":0,"end":10000}]}`} {
		s := fixtureSource()
		p := &provider{raw: []byte(raw)}
		a := Answerer{Search{Builder{s, s}}, p, time.Second}
		result := a.Answer(context.Background(), "alina", text("джира"))
		if result.Code != "LINKS_ONLY" || strings.Contains(result.Text, "Доступ уже выдан") || len(result.Sources) != 1 {
			t.Fatal(result)
		}
	}
}
func TestFragmentOffsetsAndUnsafeLinks(t *testing.T) {
	docs := demo.Documents()
	frame := Context{Documents: []demo.Document{docs[0]}}
	raw := []byte(`{"selections":[{"document_id":"doc-tracker","fragment_id":"tracker-1","start":0,"end":4}]}`)
	got, ok := grounded(frame, raw)
	if !ok || !strings.Contains(got, "«Демо»") {
		t.Fatal(got, ok)
	}
	frame.Documents[0].Stale = true
	if _, ok := grounded(frame, raw); ok {
		t.Fatal("stale excerpt accepted")
	}
	for _, link := range []string{"javascript:alert(1)", "https://user:secret@example.test", "://bad", "//evil.test/doc"} {
		if validLink(link) {
			t.Fatal(link)
		}
	}
	for _, link := range []string{"/demo/docs/doc-tracker", "https://example.test/doc"} {
		if !validLink(link) {
			t.Fatal(link)
		}
	}
}
func TestMalformedSourceFragmentsAndInstructions(t *testing.T) {
	d := demo.Documents()[0]
	d.Fragments = json.RawMessage(`bad`)
	safe := safeDocument(d)
	if string(safe.Fragments) != "[]" {
		t.Fatal(safe)
	}
	d = demo.Documents()[7]
	safe = safeDocument(d)
	if string(safe.Fragments) != "[]" {
		t.Fatal("injection remained")
	}
}

func TestAnswerDoesNotDiscloseSourcesAfterRevocation(t *testing.T) {
	s := fixtureSource()
	p := &provider{raw: []byte(`{"selections":[{"document_id":"doc-tracker","fragment_id":"tracker-1","start":0,"end":4}]}`)}
	p.during = func() { profile := s.profiles["alina"]; profile.PolicyVersion++; s.profiles["alina"] = profile }
	result := (Answerer{Search{Builder{s, s}}, p, time.Second}).Answer(context.Background(), "alina", text("джира"))
	if result.Code != "CONTEXT_UNAVAILABLE" || len(result.Sources) != 0 || strings.Contains(result.Text, "Демо»") {
		t.Fatal(result)
	}
}
func TestAnswerTimeoutUsesLinksWithoutLateText(t *testing.T) {
	s := fixtureSource()
	p := &provider{raw: []byte(`{"selections":[]}`), release: make(chan struct{}), finished: make(chan struct{})}
	result := (Answerer{Search{Builder{s, s}}, p, time.Millisecond}).Answer(context.Background(), "alina", text("джира"))
	if result.Code != "LLM_TIMEOUT" || len(result.Sources) != 1 {
		t.Fatal(result)
	}
	close(p.release)
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("late selector stuck")
	}
}

func TestAuthorizedContactsAndGoalsHaveNoInventedContent(t *testing.T) {
	s := fixtureSource()
	frame, err := (Builder{s, s}).Build(context.Background(), "alina")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ topic, resource, id string }{{"chats", "", "igor"}, {"goals", "", "olga"}, {"internship", "", "daria"}, {"resource", "space-payments", "olga"}, {"resource", "security-incidents", "igor"}, {"resource", "salary-reports", "igor"}, {"resource", "unknown", "igor"}} {
		reply := ContactFor(frame, tc.topic, tc.resource)
		if reply.Contact == nil || reply.Contact.ID != tc.id {
			t.Fatal(tc, reply)
		}
		if tc.topic == "goals" && (reply.StepState == "" || !strings.Contains(reply.Text, "Содержание целей помощник не хранит")) {
			t.Fatal("goals fabricated", reply)
		}
	}
	hidden := ContactFor(frame, "resource", "salary-reports")
	unknown := ContactFor(frame, "resource", "unknown")
	if hidden.Text != unknown.Text {
		t.Fatal("hidden resource existence disclosed")
	}
	frame.Profile.BuddyID = nil
	reply := ContactFor(frame, "buddy", "")
	if reply.Contact == nil || reply.Contact.ID != "daria" {
		t.Fatal("missing contact fallback", reply)
	}
}
