package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenRouterRequestAndBothModes(t *testing.T) {
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://openrouter.ai/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" || r.Method != "POST" {
			t.Fatal("invalid authenticated request")
		}
		var body struct {
			Model    string                           `json:"model"`
			Stream   bool                             `json:"stream"`
			Format   map[string]string                `json:"response_format"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "vendor/configured-model" || body.Stream || body.Format["type"] != "json_object" || len(body.Messages) != 2 || body.Messages[0].Role != "system" || !strings.Contains(body.Messages[1].Content, "safe question") {
			t.Fatal("invalid provider payload")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"selections\":[]}"}}]}`)), Header: http.Header{}}, nil
	})}
	p := OpenRouter{Model: "vendor/configured-model", Key: "synthetic-key", HTTP: client}
	for _, call := range []func(context.Context, Context, string) ([]byte, error){p.Recognize, p.SelectFragments} {
		raw, err := call(context.Background(), Context{}, "safe question")
		if err != nil || string(raw) != `{"selections":[]}` {
			t.Fatal(string(raw), err)
		}
	}
	if p.Mode() != "openrouter" {
		t.Fatal("wrong mode")
	}
}

func TestOpenRouterFailuresHideBodiesAndKeys(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, "private error synthetic-key"}, {402, "private credits"}, {429, "private quota"}, {500, "private upstream"}, {302, "redirect"},
		{200, `not json`}, {200, `{"choices":[]}`}, {200, `{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`},
		{200, `{"error":{"message":"private"}}`}, {200, `{"choices":[{"finish_reason":"stop","error":{"code":500},"message":{"content":"{}"}}]}`},
		{200, `{"choices":[{"finish_reason":"stop","message":{"content":"not json"}}]}`},
		{200, `{"choices":[{"finish_reason":"stop","message":{"content":"{}","tool_calls":[{}]}}]}`}, {200, strings.Repeat("x", (1<<20)+1)},
	} {
		p := OpenRouter{Model: "vendor/model", Key: "synthetic-key", HTTP: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})}}
		if _, err := p.Recognize(context.Background(), Context{}, "safe"); err != ErrProvider {
			t.Fatal("untrusted response accepted", err)
		}
	}
	for _, p := range []OpenRouter{{}, {Model: "model"}, {Key: "key"}} {
		if _, err := p.Recognize(context.Background(), Context{}, ""); err != ErrProvider {
			t.Fatal(err)
		}
	}
}

func TestOpenRouterTimeout(t *testing.T) {
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := (OpenRouter{Model: "model", Key: "key", HTTP: client}).Recognize(ctx, Context{}, "safe"); err != ErrProvider {
		t.Fatal(err)
	}
}
