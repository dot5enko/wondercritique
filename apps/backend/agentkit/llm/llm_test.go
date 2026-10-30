package llm_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
)

func TestMessageHelpers(t *testing.T) {
	m := llm.NewMessage(llm.RoleAssistant,
		llm.ReasoningBlock("meta", json.RawMessage(`{"id":"rs_1"}`), ""),
		llm.TextBlock("a"),
		llm.ToolCallBlock("c1", "f", json.RawMessage(`{}`)),
		llm.TextBlock("b"),
	)
	if m.Text() != "a\nb" {
		t.Errorf("Text = %q", m.Text())
	}
	if calls := m.ToolCalls(); len(calls) != 1 || calls[0].ToolCallID != "c1" {
		t.Errorf("ToolCalls = %+v", calls)
	}
	c := m.Clone()
	c.Blocks[0].Opaque[0] = 'X'
	if m.Blocks[0].Opaque[0] == 'X' {
		t.Error("Clone aliases Opaque")
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back llm.Message
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back, m) {
		t.Fatalf("JSON round trip = %+v, %v (%s)", back, err, raw)
	}
}

func TestUsageAdd(t *testing.T) {
	u := llm.Usage{InputTokens: 1, OutputTokens: 2}
	u.Add(llm.Usage{InputTokens: 10, CachedInputTokens: 3, OutputTokens: 20, ReasoningTokens: 5})
	if u != (llm.Usage{InputTokens: 11, CachedInputTokens: 3, OutputTokens: 22, ReasoningTokens: 5}) || u.Total() != 33 {
		t.Fatalf("usage = %+v", u)
	}
}
