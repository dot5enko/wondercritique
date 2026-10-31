package meta_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/meta"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

// TestAgentLoop exercises the full agentkit run loop through the meta adapter.
func TestAgentLoop(t *testing.T) {
	turn1 := sse(
		jsonFrame("response.output_item.added", map[string]any{"type": "response.output_item.added",
			"item": map[string]any{"type": "function_call", "call_id": "call_1", "name": "add"}}),
		jsonFrame("response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{"type": "reasoning", "id": "rs_1",
						"summary":           []any{map[string]any{"type": "summary_text", "text": "need math"}},
						"encrypted_content": "SECRET"},
					map[string]any{"type": "message", "role": "assistant", "phase": "commentary",
						"content": []any{map[string]any{"type": "output_text", "text": "calculating"}}},
					map[string]any{"type": "function_call", "call_id": "call_1", "name": "add", "arguments": `{"a":2,"b":3}`},
				},
			},
		}),
	)
	turn2 := sse(jsonFrame("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "the answer is 5"}}},
			},
		},
	}))
	var bodies []string
	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, readAll(r))
		w.Header().Set("Content-Type", "text/event-stream")
		if served == 0 {
			served++
			fmt.Fprint(w, turn1)
		} else {
			fmt.Fprint(w, turn2)
		}
	}))
	defer srv.Close()

	p := meta.New(meta.Config{APIKey: "k", BaseURL: srv.URL})
	p.SetSleep(func(context.Context, time.Duration) error { return nil })

	add := tool.New("add", "adds two numbers", func(ctx context.Context, in struct {
		A int `json:"a"`
		B int `json:"b"`
	}) (int, error) {
		return in.A + in.B, nil
	})

	a, err := agentkit.New(agentkit.Config{
		Name: "e2e", Provider: p, Store: memory.New(), Tools: tool.NewSet(add),
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.NewSession(context.Background(), agentkit.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("what is 2+3?"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "the answer is 5" {
		t.Errorf("text = %q", res.Text)
	}
	if len(res.Messages) != 3 { // assistant(+tool calls), tool result, final assistant
		t.Fatalf("messages = %d: %+v", len(res.Messages), res.Messages)
	}
	if tc := res.Messages[0].ToolCalls(); len(tc) != 1 || tc[0].ToolName != "add" {
		t.Errorf("tool calls = %+v", tc)
	}
	if tr := res.Messages[1].BlocksOf(llm.BlockToolResult); len(tr) != 1 || tr[0].Output != "5" {
		t.Errorf("tool result = %+v", res.Messages[1])
	}
	// The second request must replay the encrypted reasoning item verbatim.
	second := unmarshalBody(t, bodies[1])
	input := second["input"].([]any)
	if len(input) < 3 {
		t.Fatalf("second input too short: %v", input)
	}
	foundReasoning := false
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok || m["type"] != "reasoning" {
			continue
		}
		foundReasoning = true
		if m["encrypted_content"] != "SECRET" {
			t.Errorf("reasoning not replayed verbatim: %v", m)
		}
		if _, ok := m["summary"]; !ok {
			t.Error("reasoning replay missing summary")
		}
	}
	if !foundReasoning {
		t.Error("second request does not replay reasoning")
	}
	// tool_result must follow its function_call.
	if fc, fo := indexOf(input, "function_call"), indexOf(input, "function_call_output"); fc != -1 || fo != -1 {
		if fo < fc {
			t.Error("function_call_output precedes function_call")
		}
	}
}

func indexOf(items []any, typ string) int {
	for i, item := range items {
		if m, ok := item.(map[string]any); ok && m["type"] == typ {
			return i
		}
	}
	return -1
}

func unmarshalBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		t.Fatalf("bad body %s", raw)
	}
	return m
}

func readAll(r *http.Request) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}
