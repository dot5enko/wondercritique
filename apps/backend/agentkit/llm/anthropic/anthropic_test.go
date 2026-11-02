package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/anthropic"
)

func sse(frames ...string) string {
	var b string
	for _, f := range frames {
		b += "event: " + f[:indexByte(f)] + "\ndata: " + f[indexByte(f)+1:] + "\n\n"
	}
	return b
}

func indexByte(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			return i
		}
	}
	return -1
}

func frame(t, v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprint(t) + "|" + string(raw)
}

func newServer(t *testing.T, status int, body string, record *[]string, headers *http.Header) (*anthropic.Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if record != nil {
			*record = append(*record, string(raw))
		}
		if headers != nil {
			*headers = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if status != http.StatusOK {
			w.Header().Set("Retry-After", "0.02")
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p := anthropic.New(anthropic.Config{APIKey: "k", BaseURL: srv.URL})
	p.SetSleep(func(context.Context, time.Duration) error { return nil })
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

func TestPlainTextAndUsage(t *testing.T) {
	var bodies []string
	body := sse(
		frame("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 4},
			},
		}),
		frame("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "Hello "},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "world"},
		}),
		frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		frame("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 5},
		}),
		frame("message_stop", map[string]any{"type": "message_stop"}),
	)
	p, _ := newServer(t, 200, body, &bodies, nil)

	events, err := collect(t, p, &llm.Request{System: "be nice", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var textDelta, doneText, stop string
	var done *llm.Message
	var usage llm.Usage
	for _, ev := range events {
		switch ev.Type {
		case llm.StreamTextDelta:
			textDelta += ev.Delta
		case llm.StreamMessageDone:
			done, usage, stop = ev.Message, *ev.Usage, ev.StopReason
		}
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	doneText = done.Text()
	if textDelta != "Hello world" || doneText != "Hello world" {
		t.Errorf("text = %q done = %q", textDelta, doneText)
	}
	if usage.InputTokens != 10 || usage.CachedInputTokens != 4 || usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", usage)
	}
	if stop != llm.StopEnd {
		t.Errorf("stop = %q", stop)
	}

	// Request body shape.
	var reqBody map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &reqBody); err != nil {
		t.Fatal(err)
	}
	if reqBody["max_tokens"] != float64(8192) {
		t.Errorf("max_tokens = %v, want required default 8192", reqBody["max_tokens"])
	}
	if reqBody["system"] != "be nice" {
		t.Errorf("system = %v", reqBody["system"])
	}
	if reqBody["stream"] != true {
		t.Error("stream flag wrong")
	}
}

func TestHeadersAndEndpoint(t *testing.T) {
	var hdr http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	p := anthropic.New(anthropic.Config{APIKey: "sekrit", BaseURL: srv.URL, UserAgent: "ua/9"})
	_, _ = collect(t, p, &llm.Request{})
	if path != "/messages" {
		t.Errorf("path = %q", path)
	}
	if got := hdr.Get("x-api-key"); got != "sekrit" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := hdr.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := hdr.Get("User-Agent"); got != "ua/9" {
		t.Errorf("user-agent = %q", got)
	}
}

func TestThinkingAndToolUse(t *testing.T) {
	body := sse(
		frame("message_start", map[string]any{
			"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 7}},
		}),
		frame("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "thinking_delta", "thinking": "let me"},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "signature_delta", "signature": "SIG"},
		}),
		frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		frame("content_block_start", map[string]any{
			"type": "content_block_start", "index": 1,
			"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "add", "input": map[string]any{}},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"a":`},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `2}`},
		}),
		frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 1}),
		frame("content_block_start", map[string]any{
			"type": "content_block_start", "index": 2,
			"content_block": map[string]any{"type": "text", "text": ""},
		}),
		frame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 2,
			"delta": map[string]any{"type": "text_delta", "text": "computing"},
		}),
		frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 2}),
		frame("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use"},
			"usage": map[string]any{"output_tokens": 9},
		}),
		frame("message_stop", map[string]any{"type": "message_stop"}),
	)
	p, _ := newServer(t, 200, body, nil, nil)
	events, err := collect(t, p, &llm.Request{Messages: []llm.Message{llm.UserText("2+2?")}})
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
	if starts != 1 || argDelta != len(`{"a":2}`) {
		t.Errorf("starts=%d argDelta=%d", starts, argDelta)
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q", stop)
	}
	blocks := done.Blocks
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Type != llm.BlockReasoning || blocks[0].Text != "let me" || blocks[0].Provider != "anthropic" {
		t.Errorf("reasoning block = %+v", blocks[0])
	}
	var o struct {
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(blocks[0].Opaque, &o); err != nil || o.Thinking != "let me" || o.Signature != "SIG" {
		t.Errorf("opaque = %s (%v)", blocks[0].Opaque, err)
	}
	if blocks[1].Type != llm.BlockToolCall || blocks[1].ToolCallID != "toolu_1" ||
		blocks[1].ToolName != "add" || string(blocks[1].Input) != `{"a":2}` {
		t.Errorf("tool call = %+v", blocks[1])
	}
	if blocks[2].Type != llm.BlockText || blocks[2].Text != "computing" {
		t.Errorf("text block = %+v", blocks[2])
	}
}

func TestThinkingReplayAndBody(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &bodies, nil)
	opaque := json.RawMessage(`{"thinking":"hmm","signature":"SIG"}`)
	history := []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			llm.ReasoningBlock("anthropic", opaque, ""),
			llm.ToolCallBlock("t1", "add", json.RawMessage(`{"a":1}`)),
		}},
		{Role: llm.RoleTool, Blocks: []llm.Block{llm.ToolResultBlock("t1", "add", "2", false)}},
		llm.UserText("thanks"),
	}
	_, _ = collect(t, p, &llm.Request{Messages: history})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d: %v", len(msgs), body["messages"])
	}
	am := msgs[1].(map[string]any)
	content := am["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content = %v", content)
	}
	th := content[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "hmm" || th["signature"] != "SIG" {
		t.Errorf("thinking replay = %v", th)
	}
	tu := content[1].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "t1" {
		t.Errorf("tool_use replay = %v", tu)
	}
	tr := msgs[2].(map[string]any)
	trc := tr["content"].([]any)
	trobj := trc[0].(map[string]any)
	if tr["role"] != "user" || trobj["type"] != "tool_result" || trobj["tool_use_id"] != "t1" || trobj["content"] != "2" {
		t.Errorf("tool_result replay = %v", tr)
	}
}

func TestForeignThinkingNotReplayed(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &bodies, nil)
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			llm.ReasoningBlock("other", json.RawMessage(`{"thinking":"x","signature":"S"}`), ""),
			llm.TextBlock("done"),
		}},
		llm.UserText("next"),
	}
	_, _ = collect(t, p, &llm.Request{Messages: history})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	am := msgs[0].(map[string]any)
	for _, c := range am["content"].([]any) {
		if c.(map[string]any)["type"] == "thinking" {
			t.Errorf("foreign thinking replayed: %v", am)
		}
	}
}

func TestMaxTokensAndEffort(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &bodies, nil)
	_, _ = collect(t, p, &llm.Request{
		MaxOutputTokens: 100, ReasoningEffort: "medium",
		Messages: []llm.Message{llm.UserText("hi")},
	})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	// budget 8192 forces max_tokens above the budget.
	if body["max_tokens"] != float64(9216) {
		t.Errorf("max_tokens = %v, want budget+1024", body["max_tokens"])
	}
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(8192) {
		t.Errorf("thinking = %v", body["thinking"])
	}

	// Explicit max_tokens above the budget is kept.
	bodies = nil
	_, _ = collect(t, p, &llm.Request{
		MaxOutputTokens: 20000, ReasoningEffort: "low",
		Messages: []llm.Message{llm.UserText("hi")},
	})
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(20000) {
		t.Errorf("max_tokens = %v, want 20000", body["max_tokens"])
	}

	// Invalid effort is rejected locally.
	if _, err := collect(t, p, &llm.Request{ReasoningEffort: "bogus"}); err == nil {
		t.Error("expected error for invalid effort")
	}
}

func TestErrorEvent(t *testing.T) {
	body := sse(frame("error", map[string]any{
		"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "overloaded"},
	}))
	p, _ := newServer(t, 200, body, nil, nil)
	_, err := collect(t, p, &llm.Request{})
	var se *anthropic.StreamError
	if err == nil || !errors.As(err, &se) || se.Code != "overloaded_error" || se.Message != "overloaded" {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIErrorAndRetry(t *testing.T) {
	var n int
	turn := sse(
		frame("message_start", map[string]any{"type": "message_start",
			"message": map[string]any{"usage": map[string]any{"input_tokens": 1}}}),
		frame("message_stop", map[string]any{"type": "message_stop"}),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "rate_limit_error", "message": "slow down"},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, turn)
	}))
	defer srv.Close()
	p := anthropic.New(anthropic.Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
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

	p2, _ := newServer(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, nil, nil)
	_, err = collect(t, p2, &llm.Request{})
	var ae *anthropic.APIError
	if err == nil || !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "bad" || ae.Type != "invalid_request_error" {
		t.Fatalf("api error = %v", err)
	}
}

func TestCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // hang until the client goes away
	}))
	defer srv.Close()
	p := anthropic.New(anthropic.Config{APIKey: "k", BaseURL: srv.URL})

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

func TestImageSourceMapping(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &bodies, nil)
	msgs := []llm.Message{llm.NewMessage(llm.RoleUser,
		llm.TextBlock("look"),
		llm.ImageBlock("data:image/png;base64,AAAA"),
		llm.ImageBlock("data:image/jpeg,rawbytes"), // non-base64 data URL
	)}
	_, err := collect(t, p, &llm.Request{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %v", content)
	}
	img1 := content[1].(map[string]any)["source"].(map[string]any)
	if img1["type"] != "base64" || img1["media_type"] != "image/png" || img1["data"] != "AAAA" {
		t.Errorf("base64 source = %v", img1)
	}
	// "rawbytes" URL-unescaped then base64-encoded.
	img2 := content[2].(map[string]any)["source"].(map[string]any)
	if img2["type"] != "base64" || img2["media_type"] != "image/jpeg" {
		t.Errorf("encoded source = %v", img2)
	}
	if want := "cmF3Ynl0ZXM="; img2["data"] != want { // base64("rawbytes")
		t.Errorf("data = %v, want %v", img2["data"], want)
	}
}
