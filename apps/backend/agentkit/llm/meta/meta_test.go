package meta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/meta"
)

type frame string

func sse(frames ...frame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("event: " + f.event() + "\ndata: " + f.data() + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func (f frame) event() string {
	i := strings.IndexByte(string(f), '|')
	if i < 0 {
		return ""
	}
	return string(f[:i])
}
func (f frame) data() string {
	i := strings.IndexByte(string(f), '|')
	if i < 0 {
		return string(f)
	}
	return string(f[i+1:])
}

func jsonFrame(t string, v any) frame {
	raw, _ := json.Marshal(v)
	return frame(t + "|" + string(raw))
}

func newServer(t *testing.T, status int, body string, record *[]string) (*meta.Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if record != nil {
			*record = append(*record, string(raw))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if status != http.StatusOK {
			w.Header().Set("Retry-After", "0.02")
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p := meta.New(meta.Config{APIKey: "k", BaseURL: srv.URL})
	p.SetSleep(func(context.Context, time.Duration) error { return nil }) // no real waiting
	return p, srv
}

func collect(t *testing.T, p llm.Provider, req *llm.Request) ([]llm.StreamEvent, error) {
	t.Helper()
	var out []llm.StreamEvent
	for ev, err := range p.Stream(context.Background(), req) {
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

var plainTurn = sse(
	jsonFrame("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "Hello "}),
	jsonFrame("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "world"}),
	jsonFrame("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": "Hello world"}}},
			},
			"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 5,
				"input_tokens_details":  map[string]any{"cached_tokens": 4},
				"output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		},
	}),
)

func TestPlainText(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, plainTurn, &bodies)

	events, err := collect(t, p, &llm.Request{System: "be nice", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var textDelta, doneText string
	var done *llm.Message
	var usage llm.Usage
	for _, ev := range events {
		switch ev.Type {
		case llm.StreamTextDelta:
			textDelta += ev.Delta
		case llm.StreamMessageDone:
			done, usage = ev.Message, *ev.Usage
		}
	}
	if done != nil {
		doneText = done.Text()
	}
	if textDelta != "Hello world" || doneText != "Hello world" {
		t.Errorf("text = %q done = %q", textDelta, doneText)
	}
	if done == nil || usage.InputTokens != 10 || usage.CachedInputTokens != 4 ||
		usage.OutputTokens != 5 || usage.ReasoningTokens != 2 {
		t.Errorf("done = %+v usage %+v", done, usage)
	}

	// Request body shape.
	var body map[string]any
	if json.Unmarshal([]byte(bodies[0]), &body) != nil {
		t.Fatal("bad request body")
	}
	for _, k := range []string{"model", "stream", "store", "include", "input", "instructions"} {
		if _, ok := body[k]; !ok {
			t.Errorf("request missing %q", k)
		}
	}
	if body["stream"] != true || body["store"] != false {
		t.Error("stream/store flags wrong")
	}
	if body["instructions"] != "be nice" {
		t.Errorf("instructions = %v", body["instructions"])
	}
}

func TestToolCallStream(t *testing.T) {
	reasoningItem := map[string]any{
		"type": "reasoning", "id": "rs_1", "summary": []any{map[string]any{"type": "summary_text", "text": "thinking"}},
		"encrypted_content": "ENC",
	}
	turn := sse(
		jsonFrame("response.output_item.added", map[string]any{"type": "response.output_item.added",
			"item": map[string]any{"type": "function_call", "call_id": "call_1", "name": "weather"}}),
		jsonFrame("response.output_item.added", map[string]any{"type": "response.output_item.added",
			"item": map[string]any{"type": "function_call", "call_id": "call_2", "name": "time"}}),
		jsonFrame("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta",
			"call_id": "call_1", "delta": `{"city":`}),
		jsonFrame("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta",
			"call_id": "call_1", "delta": `"SF"}`}),
		jsonFrame("response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					reasoningItem,
					map[string]any{"type": "message", "role": "assistant", "phase": "commentary",
						"content": []any{map[string]any{"type": "output_text", "text": "let me check"}}},
					map[string]any{"type": "function_call", "call_id": "call_1", "name": "weather", "arguments": `{"city":"SF"}`},
					map[string]any{"type": "function_call", "call_id": "call_2", "name": "time", "arguments": `{}`},
				},
			},
		}),
	)
	p, _ := newServer(t, 200, turn, nil)
	events, err := collect(t, p, &llm.Request{Messages: []llm.Message{llm.UserText("weather?")}})
	if err != nil {
		t.Fatal(err)
	}
	var starts, argDelta int
	var done *llm.Message
	stop := ""
	for _, ev := range events {
		switch ev.Type {
		case llm.StreamToolCallStart:
			starts++
		case llm.StreamToolCallDelta:
			argDelta += len(ev.Delta)
		case llm.StreamMessageDone:
			done, stop = ev.Message, ev.StopReason
		}
	}
	if starts != 2 || argDelta != len(`{"city":"SF"}`) {
		t.Errorf("starts=%d argDelta=%d", starts, argDelta)
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q", stop)
	}
	blocks := done.Blocks
	if len(blocks) != 4 {
		t.Fatalf("blocks = %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != llm.BlockReasoning || string(blocks[0].Opaque) == "" || blocks[0].Text != "thinking" {
		t.Errorf("reasoning block = %+v", blocks[0])
	}
	if blocks[1].Type != llm.BlockText || blocks[1].Phase != llm.PhaseCommentary {
		t.Errorf("commentary block = %+v", blocks[1])
	}
	if blocks[2].Type != llm.BlockToolCall || blocks[2].ToolCallID != "call_1" || blocks[2].ToolName != "weather" ||
		string(blocks[2].Input) != `{"city":"SF"}` {
		t.Errorf("tool call = %+v", blocks[2])
	}
	if blocks[3].ToolCallID != "call_2" || string(blocks[3].Input) != `{}` {
		t.Errorf("tool call 2 = %+v", blocks[3])
	}
	// The opaque reasoning payload must round-trip verbatim.
	rawWant, _ := json.Marshal(reasoningItem)
	if string(blocks[0].Opaque) != string(rawWant) {
		t.Errorf("opaque = %s\nwant %s", blocks[0].Opaque, rawWant)
	}
}

func TestReasoningReplayAndStructure(t *testing.T) {
	reasoning := llm.ReasoningBlock("meta", json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}`), "")
	msgs := []llm.Message{
		llm.UserText("q1"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{reasoning, llm.TextBlock("done"), llm.ToolCallBlock("c1", "f", json.RawMessage(`{}`))}},
		{Role: llm.RoleTool, Blocks: []llm.Block{llm.ToolResultBlock("c1", "f", "out", false)}},
		llm.UserText("q2"),
	}
	items, err := meta.BuildInput(msgs)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(items, "", "  ")
	t.Log(string(raw))
	if len(items) != 6 {
		t.Fatalf("items = %d:\n%s", len(items), raw)
	}
	// reasoning replayed verbatim (payload fields preserved) with summary ensured
	reason, ok := items[1]["encrypted_content"].(string)
	if !ok || reason != "ENC" || items[1]["id"] != "rs_1" {
		t.Errorf("reasoning replay = %v", items[1])
	}
	if _, ok := items[1]["summary"]; !ok {
		t.Error("summary missing on replayed reasoning item")
	}
	// commentary before function_call
	if items[2]["phase"] != llm.PhaseCommentary {
		t.Errorf("assistant text phase = %v", items[2]["phase"])
	}
	// function_call + output pairing
	if items[3]["type"] != "function_call" || items[4]["type"] != "function_call_output" {
		t.Errorf("pairing wrong: %v, %v", items[3]["type"], items[4]["type"])
	}
	// structure: reasoning must be followed by assistant message or function_call
	if isReasoning(items[0]) {
		t.Error("no reasoning should start the conversation")
	}
}

func isReasoning(item map[string]any) bool {
	t, _ := item["type"].(string)
	return t == "reasoning"
}

func TestDanglingReasoning(t *testing.T) {
	reasoning := llm.ReasoningBlock("meta", json.RawMessage(`{"type":"reasoning","encrypted_content":"E"}`), "")
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{reasoning}},
		llm.UserText("next"),
	}
	items, err := meta.BuildInput(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || isReasoning(items[1]) {
		t.Fatalf("dangling reasoning not fixed: %v", items)
	}
	if items[1]["type"] != "message" || items[1]["role"] != "assistant" {
		t.Errorf("inserted item = %v", items[1])
	}
}

func TestCallIDShortening(t *testing.T) {
	long := strings.Repeat("x", 100)
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.ToolCallBlock(long, "f", json.RawMessage(`{}`))}},
		{Role: llm.RoleTool, Blocks: []llm.Block{llm.ToolResultBlock(long, "f", "out", false)}},
	}
	items, err := meta.BuildInput(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if items[0]["call_id"] != items[1]["call_id"] || len(items[0]["call_id"].(string)) > 64 {
		t.Errorf("call ids inconsistent or too long: %v vs %v", items[0]["call_id"], items[1]["call_id"])
	}
}

func TestFailedEvent(t *testing.T) {
	turn := sse(
		jsonFrame("response.failed", map[string]any{"type": "response.failed",
			"response": map[string]any{"error": map[string]any{"code": "server_error", "message": "boom"}}}),
	)
	p, _ := newServer(t, 200, turn, nil)
	_, err := collect(t, p, &llm.Request{})
	var se *meta.StreamError
	if err == nil || !errors.As(err, &se) || se.Code != "server_error" || se.Message != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIErrorAndRetry(t *testing.T) {
	var n int
	turn := sse(jsonFrame("response.completed", map[string]any{"type": "response.completed",
		"response": map[string]any{"status": "completed", "output": []any{
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		}}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"type": "rate_limit_error", "code": "rate_limit_exceeded", "message": "slow down"}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, turn)
	}))
	defer srv.Close()
	p := meta.New(meta.Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
	p.SetSleep(func(context.Context, time.Duration) error { return nil })

	events, err := collect(t, p, &llm.Request{})
	if err != nil {
		t.Fatalf("after retry: %v", err)
	}
	if n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if len(events) == 0 || events[len(events)-1].Type != llm.StreamMessageDone {
		t.Errorf("events = %+v", events)
	}

	// Hard API error surfaces parsed fields.
	p2, _ := newServer(t, 400, `{"error":{"type":"invalid_request_error","param":"input","message":"bad"}}`, nil)
	_, err = collect(t, p2, &llm.Request{})
	var ae *meta.APIError
	if err == nil || !errors.As(err, &ae) || ae.Status != 400 || ae.Param != "input" || ae.Message != "bad" {
		t.Fatalf("api error = %v", err)
	}
	if ae.Temporary() {
		t.Error("400 should not be retryable")
	}
}

func TestCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // hang until the client goes away
	}))
	defer srv.Close()
	p := meta.New(meta.Config{APIKey: "k", BaseURL: srv.URL})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	var got error
	for _, err := range p.Stream(ctx, &llm.Request{}) {
		if err != nil {
			got = err
		}
	}
	if got == nil || !errors.Is(got, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got)
	}
}

func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	raw, _ := json.Marshal(got)
	if string(raw) != want {
		t.Errorf("json = %s\nwant  %s", raw, want)
	}
}
