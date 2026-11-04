package agentrun

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// recSink records what the tools hand to a pipeline step.
type recSink struct {
	mu    sync.Mutex
	comps []Component
	subs  int
}

func (s *recSink) Artifact(context.Context, string, string, []byte, *float64) (string, error) {
	return "art", nil
}
func (s *recSink) SubRun(*NodeResult) { s.mu.Lock(); s.subs++; s.mu.Unlock() }
func (s *recSink) Event(string, any)  {}
func (s *recSink) Activity(string)    {}
func (s *recSink) Component(_ context.Context, c Component) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comps = append(s.comps, c)
	return "comp", nil
}

func TestGenComponentsKeepsEveryComponent(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	ms := h.seedModelSet()
	worker := h.seedAgent("gen", ms.ID, "artifact", true)
	body := studio.AgentBody{
		ModelSetID: ms.ID, SystemPrompt: "chat", InputTemplate: "{{brief}}",
		Tools:          []string{"gen_components"},
		OutputContract: studio.OutputContract{Type: "chat"},
		ToolConfig:     &studio.ToolConfig{GenComponents: &studio.GenerateToolConfig{AgentID: worker.ID}},
	}
	raw, _ := json.Marshal(body)
	chat, err := h.store.Create(ctx, studio.KindAgent, studio.DefIdentity{Slug: "chat", Name: "chat"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.Publish(ctx, studio.KindAgent, chat.ID, "", "t", raw); err != nil {
		t.Fatal(err)
	}

	h.prov.Push(
		fake.ToolCalls(fake.Call("g1", "gen_components", map[string]any{
			"count": 3, "description": "a primary button", "folder": "Buttons", "names": []string{"a.html", "b.html"},
		})),
		fake.ToolCalls(fake.Call("w1", "submit_artifact", map[string]string{"html": "<button class='x'>1</button>"})),
		fake.ToolCalls(fake.Call("w2", "submit_artifact", map[string]string{"html": "<button class='x'>2</button>"})),
		fake.ToolCalls(fake.Call("w3", "submit_artifact", map[string]string{"html": "<button class='x'>3</button>"})),
		fake.Text("Here are 3 buttons."),
	)
	sink := &recSink{}
	res, err := h.runtime.RunNode(WithSink(ctx, sink), NodeRequest{AgentID: chat.ID, Samples: map[string]any{"brief": "give me 3 buttons", "canvas": "## Canvas"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Contract.Type != "chat" || res.Text != "Here are 3 buttons." {
		t.Fatalf("result = %q (%s)", res.Text, res.Contract.Type)
	}
	if len(sink.comps) != 3 || sink.subs != 3 {
		t.Fatalf("components = %d, sub-runs = %d", len(sink.comps), sink.subs)
	}
	var htmls []string
	for _, c := range sink.comps {
		htmls = append(htmls, c.HTML)
		if c.Folder != "Buttons" {
			t.Errorf("folder = %q", c.Folder)
		}
	}
	sort.Strings(htmls)
	if !strings.Contains(htmls[0], ">1<") || !strings.Contains(htmls[2], ">3<") {
		t.Errorf("htmls = %v", htmls)
	}
	if sink.comps[0].Name != "a.html" || sink.comps[1].Name != "b.html" || sink.comps[2].Name != "" {
		t.Errorf("names = %q %q %q", sink.comps[0].Name, sink.comps[1].Name, sink.comps[2].Name)
	}

	// Workers got the description as their brief, not the chat's canvas.
	reqs := h.prov.Requests()
	workers := 0
	for _, r := range reqs[1:4] {
		in := r.Messages[0].Text()
		if strings.HasPrefix(in, "Brief: a primary button") && !strings.Contains(in, "## Canvas") {
			workers++
		}
	}
	if workers != 3 {
		t.Errorf("worker inputs = %d/3 ok", workers)
	}
	// The chat agent saw the tool result listing the components.
	last := reqs[len(reqs)-1].Messages
	var toolOut string
	for _, m := range last {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				toolOut = b.Output
			}
		}
	}
	if !strings.Contains(toolOut, "/work/components/a.html") || !strings.Contains(toolOut, "3 component(s) created") {
		t.Errorf("tool output = %s", toolOut)
	}
}

func TestGenComponentsDefaultsToOne(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	ms := h.seedModelSet()
	worker := h.seedAgent("gen", ms.ID, "artifact", true)
	body := studio.AgentBody{
		ModelSetID: ms.ID, SystemPrompt: "chat", InputTemplate: "{{brief}}",
		Tools:          []string{"gen_components"},
		OutputContract: studio.OutputContract{Type: "chat"},
		ToolConfig:     &studio.ToolConfig{GenComponents: &studio.GenerateToolConfig{AgentID: worker.ID}},
	}
	raw, _ := json.Marshal(body)
	chat, _ := h.store.Create(ctx, studio.KindAgent, studio.DefIdentity{Slug: "chat", Name: "chat"}, raw)
	if _, _, err := h.store.Publish(ctx, studio.KindAgent, chat.ID, "", "t", raw); err != nil {
		t.Fatal(err)
	}
	h.prov.Push(
		fake.ToolCalls(fake.Call("g1", "gen_components", map[string]any{"description": "a hero"})),
		fake.ToolCalls(fake.Call("w1", "submit_artifact", map[string]string{"html": "<section class='h'>hero</section>"})),
		fake.Text("Done."),
	)
	sink := &recSink{}
	if _, err := h.runtime.RunNode(WithSink(ctx, sink), NodeRequest{AgentID: chat.ID, Samples: map[string]any{"brief": "a hero"}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.comps) != 1 {
		t.Fatalf("components = %d", len(sink.comps))
	}
	// A single component gets no "candidate 1 of 1" variation note.
	if in := h.prov.Requests()[1].Messages[0].Text(); strings.Contains(in, "Variation") {
		t.Errorf("worker input = %q", in)
	}
}
