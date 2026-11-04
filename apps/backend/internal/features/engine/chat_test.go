package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// chatScript makes canvas-chat create `count` components through the step's
// sink (what gen_components does) and reply with text.
func (h *harness) chatScript(count int, folder string, names ...string) {
	h.agents.ctxHandler = func(ctx context.Context, slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		if slug != studio.ChatAgentSlug {
			return nil, fmt.Errorf("unexpected agent %q", slug)
		}
		sink := agentrun.SinkFrom(ctx)
		if sink == nil {
			return nil, fmt.Errorf("no sink")
		}
		for i := range count {
			name := ""
			if i < len(names) {
				name = names[i]
			}
			if _, err := sink.Component(ctx, agentrun.Component{HTML: fmt.Sprintf("<button class='b%d'>%d</button>", i, i), Name: name, Folder: folder}); err != nil {
				return nil, err
			}
		}
		return &agentrun.NodeResult{
			Contract: studio.OutputContract{Type: "chat"}, Text: fmt.Sprintf("Made %d buttons (message %d).", count, n),
			SessionID: "sess-chat", Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}, ProviderID: "meta", ModelID: "muse", AgentVersion: 1,
		}, nil
	}
}

func TestChatAgentKeepsEveryComponent(t *testing.T) {
	h := setup(t)
	h.chatScript(5, "Buttons", "primary.html", "ghost.html")
	id, w := h.start(h.user, `{"agent":"canvas-chat","input":{"brief":"give me 5 buttons"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCompleted || run.Mode != models.RunModeAgent || run.AgentID == "" || run.PipelineID != "" {
		t.Fatalf("run = %s %s agent=%q pipeline=%q err=%q", run.Status, run.Mode, run.AgentID, run.PipelineID, run.Error)
	}
	if run.Reply != "Made 5 buttons (message 1)." {
		t.Errorf("reply = %q", run.Reply)
	}
	d := h.detail(id)
	if len(d.Results) != 5 || d.Mode != "agent" || d.Reply != run.Reply || len(d.Steps) != 2 {
		t.Fatalf("detail: results=%d mode=%s reply=%q steps=%d", len(d.Results), d.Mode, d.Reply, len(d.Steps))
	}
	paths := h.canvasPaths(id)
	want := []string{"/buttons/5-buttons-3.html", "/buttons/5-buttons-4.html", "/buttons/5-buttons-5.html", "/buttons/ghost.html", "/buttons/primary.html"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("canvas = %v", paths)
	}
	// The chat agent got the message as its brief and no pipeline was involved.
	calls := h.agents.callsOf(studio.ChatAgentSlug)
	if len(calls) != 1 || calls[0].Samples["brief"] != "give me 5 buttons" || calls[0].Version != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if _, ok := calls[0].Samples["history"]; ok {
		t.Error("the first message has no history")
	}

	// A follow-up sees the conversation and the canvas.
	h.chatScript(0, "")
	second, w := h.start(h.user, `{"agent":"canvas-chat","thread_id":"`+id+`","input":{"brief":"which one is best?"}}`)
	if second == "" {
		t.Fatalf("follow-up: %d %s", w.Code, w.Body)
	}
	run2 := h.waitDone(second)
	if run2.Status != models.RunCompleted || run2.Reply == "" {
		t.Fatalf("follow-up = %s %q %q", run2.Status, run2.Reply, run2.Error)
	}
	calls = h.agents.callsOf(studio.ChatAgentSlug)
	hist, _ := calls[1].Samples["history"].(string)
	for _, s := range []string{"### Message 1", "User: give me 5 buttons", "You: Made 5 buttons", "/buttons/primary.html"} {
		if !strings.Contains(hist, s) {
			t.Errorf("history lacks %q:\n%s", s, hist)
		}
	}
	canvasText, _ := calls[1].Samples["canvas"].(string)
	if !strings.Contains(canvasText, "### Where new components go") || !strings.Contains(canvasText, "gen_components") {
		t.Errorf("canvas = %s", canvasText)
	}
	// A reply-only message commits nothing.
	if d := h.detail(second); len(d.Results) != 0 || len(d.CanvasPaths) != 0 {
		t.Errorf("reply-only results = %d paths = %v", len(d.Results), d.CanvasPaths)
	}
}

func TestChatAgentStartValidation(t *testing.T) {
	h := setup(t)
	if _, w := h.start(h.user, `{"agent":"nope","input":{"brief":"x"}}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent: %d", w.Code)
	}
	if _, w := h.start(h.user, `{"agent":"canvas-chat","pipeline_id":"p","input":{"brief":"x"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("agent + pipeline: %d", w.Code)
	}
}
