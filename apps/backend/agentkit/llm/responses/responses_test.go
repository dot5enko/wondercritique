package responses_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/responses"
)

func TestGenericAdapterRoundTrip(t *testing.T) {
	turn := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\ndata: " + mustJSON(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{},
						"encrypted_content": "ENC"},
					map[string]any{"type": "function_call", "call_id": "c1", "name": "f", "arguments": `{}`},
				},
				"usage": map[string]any{"input_tokens": 3, "output_tokens": 2},
			},
		}) + "\n\n"
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(turn))
	}))
	defer srv.Close()

	p := responses.New(responses.Config{APIKey: "k", BaseURL: srv.URL, Name: "acme"})
	if p.Name() != "acme" {
		t.Errorf("name = %q", p.Name())
	}
	var done *llm.Message
	for ev, err := range p.Stream(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserText("q")}}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.StreamMessageDone {
			done = ev.Message
		}
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	// Reasoning blocks carry the configured identity.
	if done.Blocks[0].Provider != "acme" {
		t.Errorf("provider tag = %q", done.Blocks[0].Provider)
	}

	// Replay via a second request: same-identity reasoning is replayed, foreign is dropped.
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			llm.ReasoningBlock("acme", done.Blocks[0].Opaque, ""),
			llm.ReasoningBlock("other", json.RawMessage(`{"type":"reasoning","id":"x","encrypted_content":"E"}`), ""),
			llm.ToolCallBlock("c1", "f", json.RawMessage(`{}`)),
		}},
		{Role: llm.RoleTool, Blocks: []llm.Block{llm.ToolResultBlock("c1", "f", "out", false)}},
	}
	items, err := responses.BuildInputAs("acme", history)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d: %v", len(items), items)
	}
	if items[0]["encrypted_content"] != "ENC" {
		t.Errorf("replay = %v", items[0])
	}

	// Default identity when Name is empty.
	p2 := responses.New(responses.Config{BaseURL: "http://unused.invalid"})
	if p2.Name() != "responses" {
		t.Errorf("default name = %q", p2.Name())
	}
}

func TestInvalidEffort(t *testing.T) {
	p := responses.New(responses.Config{})
	_, err := collect(p, &llm.Request{ReasoningEffort: "none"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("err = %v", err)
	}
	_, err = collect(p, &llm.Request{ReasoningEffort: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "invalid reasoning effort") {
		t.Errorf("err = %v", err)
	}
}

func TestAPIErrorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"nope"}}`))
	}))
	defer srv.Close()
	p := responses.New(responses.Config{BaseURL: srv.URL})
	_, err := collect(p, &llm.Request{})
	var ae *responses.APIError
	if err == nil || !errors.As(err, &ae) || ae.Status != 401 || ae.Type != "authentication_error" {
		t.Fatalf("err = %v", err)
	}
}

func collect(p llm.Provider, req *llm.Request) ([]llm.StreamEvent, error) {
	var out []llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
