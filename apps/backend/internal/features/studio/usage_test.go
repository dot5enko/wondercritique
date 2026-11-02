package studio

import (
	"context"
	"net/http"
	"testing"
)

func TestAgentUsageAndDelete(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	mk := func(kind, slug string, body any) string {
		def, err := h.store.Create(ctx, kind, DefIdentity{Slug: slug, Name: slug}, []byte(marshal(t, body)))
		if err != nil {
			t.Fatal(err)
		}
		return def.ID
	}
	ms := mk(KindModelSet, "ms", okModelSet())
	used := mk(KindAgent, "used", okAgent(ms))
	unused := mk(KindAgent, "unused", okAgent(ms))

	pipe := PipelineBody{Nodes: []PipelineNode{
		{ID: "a", Type: "agent", Config: map[string]any{"agent_id": used}},
	}}
	pid := mk(KindPipeline, "p", pipe)

	var out struct {
		Usage map[string][]PipelineRef `json:"usage"`
	}
	h.get(t, "/studio/agents/usage", &out)
	if len(out.Usage[used]) != 1 || out.Usage[used][0].ID != pid || len(out.Usage[unused]) != 0 {
		t.Fatalf("usage = %+v", out.Usage)
	}

	// Used agent: 409, still present.
	if w := h.do(http.MethodDelete, "/studio/agent/"+used, ""); w.Code != http.StatusConflict {
		t.Fatalf("delete used = %d %s", w.Code, w.Body)
	}
	if _, err := h.store.Get(ctx, KindAgent, used); err != nil {
		t.Fatalf("used agent vanished: %v", err)
	}

	// Unused agent: 204, gone, slug reusable.
	if w := h.do(http.MethodDelete, "/studio/agent/"+unused, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete unused = %d %s", w.Code, w.Body)
	}
	if _, err := h.store.Get(ctx, KindAgent, unused); err == nil {
		t.Fatal("unused agent still exists")
	}
	mk(KindAgent, "unused", okAgent(ms))

	// Other kinds can't be deleted.
	if w := h.do(http.MethodDelete, "/studio/pipeline/"+pid, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("delete pipeline = %d", w.Code)
	}

	// Dropping the node from the pipeline frees the agent.
	if _, err := h.store.PutDraft(ctx, KindPipeline, pid, []byte(marshal(t, PipelineBody{}))); err != nil {
		t.Fatal(err)
	}
	if w := h.do(http.MethodDelete, "/studio/agent/"+used, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete freed = %d %s", w.Code, w.Body)
	}
}
