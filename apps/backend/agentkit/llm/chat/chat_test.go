package chat_test

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
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/chat"
)

func sseData(v any) string {
	raw, _ := json.Marshal(v)
	return "data: " + string(raw) + "\n\n"
}

func newServer(t *testing.T, status int, body string, record *[]string, headers *http.Header) (*chat.Provider, *httptest.Server) {
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
	p := chat.New(chat.Config{APIKey: "k", BaseURL: srv.URL})
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

func TestPlainTextAndUsage(t *testing.T) {
	var bodies []string
	body := sseData(map[string]any{
		"choices": []any{map[string]any{
			"delta": map[string]any{"content": "Hello "},
		}},
	}) + sseData(map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": "world"},
			"finish_reason": "stop",
		}},
	}) + sseData(map[string]any{
		"usage": map[string]any{
			"prompt_tokens": 10, "completion_tokens": 5,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 4},
			"completion_tokens_details": map[string]any{"reasoning_tokens": 2},
		},
	}) + "data: [DONE]\n\n"
	p, _ := newServer(t, 200, body, &bodies, nil)

	events, err := collect(t, p, &llm.Request{System: "be nice", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var textDelta, doneText string
	var done *llm.Message
	var usage llm.Usage
	var stop string
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
	if usage.InputTokens != 10 || usage.CachedInputTokens != 4 ||
		usage.OutputTokens != 5 || usage.ReasoningTokens != 2 {
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
	if reqBody["stream"] != true {
		t.Error("stream flag wrong")
	}
	so, ok := reqBody["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %v", reqBody["stream_options"])
	}
	if reqBody["model"] != "" {
		t.Errorf("model = %v, want empty (no DefaultModel configured)", reqBody["model"])
	}
	msgs := reqBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be nice" {
		t.Errorf("system message = %v", sys)
	}
}

func TestDefaultModelMaxTokensEffort(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "data: [DONE]\n\n", &bodies, nil)
	_, _ = collect(t, p, &llm.Request{
		MaxOutputTokens: 123, ReasoningEffort: "High",
		Messages: []llm.Message{llm.UserText("hi")},
	})
	var reqBody map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &reqBody); err != nil {
		t.Fatal(err)
	}
	if reqBody["max_tokens"] != float64(123) {
		t.Errorf("max_tokens = %v", reqBody["max_tokens"])
	}
	if reqBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v", reqBody["reasoning_effort"])
	}

	// DefaultModel is used when the request model is empty.
	p2 := chat.New(chat.Config{APIKey: "k", BaseURL: "http://unused.invalid", DefaultModel: "m-1"})
	p2.SetSleep(func(context.Context, time.Duration) error { return nil })
	_ = p2 // BaseURL unreachable: assert via bodies of a second server instead
	srvBody := func(h func([]byte)) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			h(raw)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		t.Cleanup(s.Close)
		return s
	}
	srv := srvBody(func(raw []byte) {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			bodies = append(bodies, string(raw))
		}
	})
	p3 := chat.New(chat.Config{BaseURL: srv.URL, DefaultModel: "m-1"})
	p3.SetSleep(func(context.Context, time.Duration) error { return nil })
	_, _ = collect(t, p3, &llm.Request{})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "m-1" {
		t.Errorf("model = %v, want DefaultModel", body["model"])
	}
}

func TestToolCallsParallel(t *testing.T) {
	body := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "weather", "arguments": ""}},
			map[string]any{"index": 1, "id": "call_2", "type": "function",
				"function": map[string]any{"name": "time", "arguments": ""}},
		}},
	}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": `{"city":`}},
			}},
		}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": `"SF"}`}},
				map[string]any{"index": 1, "function": map[string]any{"arguments": `{}`}},
			}},
		}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{
			"delta":         map[string]any{},
			"finish_reason": "tool_calls",
		}}}) +
		"data: [DONE]\n\n"
	p, _ := newServer(t, 200, body, nil, nil)

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
	if starts != 2 || argDelta != len(`{"city":"SF"}`)+len(`{}`) {
		t.Errorf("starts=%d argDelta=%d", starts, argDelta)
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	if stop != llm.StopToolUse {
		t.Errorf("stop = %q", stop)
	}
	blocks := done.ToolCalls()
	if len(blocks) != 2 {
		t.Fatalf("tool calls = %+v", blocks)
	}
	if blocks[0].ToolCallID != "call_1" || blocks[0].ToolName != "weather" || string(blocks[0].Input) != `{"city":"SF"}` {
		t.Errorf("tool call 1 = %+v", blocks[0])
	}
	if blocks[1].ToolCallID != "call_2" || blocks[1].ToolName != "time" || string(blocks[1].Input) != `{}` {
		t.Errorf("tool call 2 = %+v", blocks[1])
	}
}

func TestReasoningContentRoundTrip(t *testing.T) {
	var bodies []string
	body := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"reasoning_content": "thinking hard"},
	}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"content": "answer"},
		}}}) +
		sseData(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{}, "finish_reason": "stop",
		}}}) +
		"data: [DONE]\n\n"
	p, _ := newServer(t, 200, body, &bodies, nil)

	events, err := collect(t, p, &llm.Request{Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var done *llm.Message
	for _, ev := range events {
		if ev.Type == llm.StreamMessageDone {
			done = ev.Message
		}
	}
	if done == nil {
		t.Fatal("no message_done")
	}
	if len(done.Blocks) != 2 {
		t.Fatalf("blocks = %+v", done.Blocks)
	}
	rb := done.Blocks[0]
	if rb.Type != llm.BlockReasoning || rb.Text != "thinking hard" || rb.Provider != "chat" {
		t.Errorf("reasoning block = %+v", rb)
	}
	var o struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal(rb.Opaque, &o); err != nil || o.ReasoningContent != "thinking hard" {
		t.Errorf("opaque = %s (%v)", rb.Opaque, err)
	}

	// Replay: the reasoning content is sent back as reasoning_content on the
	// assistant message of a follow-up request.
	p2, _ := newServer(t, 200, "data: [DONE]\n\n", &bodies, nil)
	history := []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			llm.ReasoningBlock("chat", rb.Opaque, ""),
			llm.ToolCallBlock("c1", "f", json.RawMessage(`{}`)),
		}},
		{Role: llm.RoleTool, Blocks: []llm.Block{llm.ToolResultBlock("c1", "f", "out", false)}},
		llm.UserText("next"),
	}
	_, _ = collect(t, p2, &llm.Request{Messages: history})
	var body2 map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &body2); err != nil {
		t.Fatal(err)
	}
	list := body2["messages"].([]any)
	// [user, assistant(tool call) with reasoning_content, tool, user] — no system.
	if len(list) != 4 {
		t.Fatalf("messages = %d: %v", len(list), body2["messages"])
	}
	am := list[1].(map[string]any)
	if am["reasoning_content"] != "thinking hard" {
		t.Errorf("reasoning_content not replayed: %v", am)
	}
	tcs := am["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls = %v", tcs)
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "c1" {
		t.Errorf("tool call = %v", tc)
	}
	tm := list[2].(map[string]any)
	if tm["role"] != "tool" || tm["tool_call_id"] != "c1" || tm["content"] != "out" {
		t.Errorf("tool message = %v", tm)
	}
}

func TestForeignReasoningNotReplayed(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "data: [DONE]\n\n", &bodies, nil)
	history := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			llm.ReasoningBlock("other", json.RawMessage(`{"reasoning_content":"x"}`), ""),
			llm.TextBlock("done"),
		}},
		llm.UserText("next"),
	}
	_, _ = collect(t, p, &llm.Request{Messages: history})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	list := body["messages"].([]any)
	am := list[0].(map[string]any)
	if _, has := am["reasoning_content"]; has {
		t.Errorf("foreign reasoning replayed: %v", am)
	}
}

func TestUserMultipartContent(t *testing.T) {
	var bodies []string
	p, _ := newServer(t, 200, "data: [DONE]\n\n", &bodies, nil)
	reqMsgs := []llm.Message{llm.NewMessage(llm.RoleUser,
		llm.TextBlock("look"), llm.ImageBlock("data:image/png;base64,AAA"))}
	_, _ = collect(t, p, &llm.Request{Messages: reqMsgs})
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	list := body["messages"].([]any)
	m := list[0].(map[string]any)
	parts, ok := m["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %v", m["content"])
	}
	if parts[0].(map[string]any)["type"] != "text" {
		t.Errorf("part 0 = %v", parts[0])
	}
	img := parts[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Errorf("part 1 = %v", img)
	}
}

func TestAPIErrorAndRetry(t *testing.T) {
	var n int
	turn := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": "ok"}, "finish_reason": "stop",
	}}}) + "data: [DONE]\n\n"
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
	p := chat.New(chat.Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
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
	p2, _ := newServer(t, 400, `{"error":{"type":"invalid_request_error","param":"messages","message":"bad"}}`, nil, nil)
	_, err = collect(t, p2, &llm.Request{})
	var ae *chat.APIError
	if err == nil || !errors.As(err, &ae) || ae.Status != 400 || ae.Param != "messages" || ae.Message != "bad" {
		t.Fatalf("api error = %v", err)
	}
	if ae.Temporary() {
		t.Error("400 should not be retryable")
	}
}

func TestInStreamError(t *testing.T) {
	body := sseData(map[string]any{"error": map[string]any{"code": "server_error", "message": "boom"}})
	p, _ := newServer(t, 200, body, nil, nil)
	_, err := collect(t, p, &llm.Request{})
	var se *chat.StreamError
	if err == nil || !errors.As(err, &se) || se.Code != "server_error" || se.Message != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestStopLength(t *testing.T) {
	body := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": "trunca"}, "finish_reason": "length",
	}}}) + "data: [DONE]\n\n"
	p, _ := newServer(t, 200, body, nil, nil)
	events, err := collect(t, p, &llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var stop string
	for _, ev := range events {
		if ev.Type == llm.StreamMessageDone {
			stop = ev.StopReason
		}
	}
	if stop != llm.StopMaxTokens {
		t.Errorf("stop = %q", stop)
	}
}

func TestHeadersAndEndpoint(t *testing.T) {
	var hdr http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := chat.New(chat.Config{APIKey: "sekrit", BaseURL: srv.URL, UserAgent: "ua/9"})
	_, _ = collect(t, p, &llm.Request{})
	if path != "/chat/completions" {
		t.Errorf("path = %q", path)
	}
	if got := hdr.Get("Authorization"); got != "Bearer sekrit" {
		t.Errorf("authorization = %q", got)
	}
	if got := hdr.Get("User-Agent"); got != "ua/9" {
		t.Errorf("user-agent = %q", got)
	}
	if got := hdr.Get("Accept"); got != "text/event-stream" {
		t.Errorf("accept = %q", got)
	}
}

func TestCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // hang until the client goes away
	}))
	defer srv.Close()
	p := chat.New(chat.Config{APIKey: "k", BaseURL: srv.URL})

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

func TestMaxRetriesDisabled(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"down"}}`)
	}))
	defer srv.Close()
	p := chat.New(chat.Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: -1})
	_, err := collect(t, p, &llm.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v", err)
	}
}
