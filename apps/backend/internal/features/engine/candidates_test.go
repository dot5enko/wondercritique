package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// candidatePipeline drafts input -> gen(count=3) [-> pick_best -> out] and
// returns the pipeline id.
func (h *harness) candidatePipeline(withPick bool) string {
	h.t.Helper()
	ctx := context.Background()
	def, _, err := h.defs.LatestPublished(ctx, studio.KindPipeline, "default")
	if err != nil {
		h.t.Fatal(err)
	}
	gen, _ := h.defs.GetBySlug(ctx, studio.KindAgent, "component-generator")
	body := studio.PipelineBody{
		Nodes: []studio.PipelineNode{
			{ID: "in", Type: "input", Config: map[string]any{}},
			{ID: "gen", Type: "agent", Config: map[string]any{"agent_id": gen.ID, "count": 3.0}},
			{ID: "out", Type: "output", Config: map[string]any{}},
		},
		Edges: []studio.PipelineEdge{
			{ID: "a", From: studio.EdgeEndpoint{Node: "in", Port: "brief"}, To: studio.EdgeEndpoint{Node: "gen", Port: "brief"}},
		},
	}
	if withPick {
		body.Nodes = append(body.Nodes, studio.PipelineNode{ID: "pick", Type: "pick_best", Config: map[string]any{}})
		body.Edges = append(body.Edges,
			studio.PipelineEdge{ID: "b", From: studio.EdgeEndpoint{Node: "gen", Port: "candidates"}, To: studio.EdgeEndpoint{Node: "pick", Port: "candidates"}},
			studio.PipelineEdge{ID: "c", From: studio.EdgeEndpoint{Node: "pick", Port: "html"}, To: studio.EdgeEndpoint{Node: "out", Port: "html"}},
		)
	}
	raw, _ := json.Marshal(body)
	if _, err := h.defs.PutDraft(ctx, studio.KindPipeline, def.ID, raw); err != nil {
		h.t.Fatal(err)
	}
	return def.ID
}

func (h *harness) candidateScript() {
	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		switch slug {
		case "component-generator":
			return htmlResult(fmt.Sprintf("<div>design %s</div>", req.Samples["variation"])), nil
		case "vision-judge":
			res := htmlResult("")
			res.Contract = studio.OutputContract{Type: "custom"}
			res.Fields = json.RawMessage(`{"winner":2,"ranking":[{"candidate":2,"score":8,"reason":"best"},{"candidate":1,"score":5,"reason":"ok"},{"candidate":3,"score":4,"reason":"meh"}],"summary":"two wins"}`)
			return res, nil
		}
		return nil, fmt.Errorf("unexpected agent %q", slug)
	}
}

func TestAgentCountGivesMultipleResults(t *testing.T) {
	h := setup(t)
	h.candidateScript()
	pid := h.candidatePipeline(false)
	id, w := h.start(h.admin, `{"is_test":true,"pipeline_id":"`+pid+`","input":{"brief":"5 buttons"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Error)
	}
	d := h.detail(id)
	if len(d.Results) != 3 {
		t.Fatalf("results = %+v", d.Results)
	}
	for i, r := range d.Results {
		if r.Position != i {
			t.Errorf("result %d position %d", i, r.Position)
		}
	}
	if len(h.agents.callsOf("component-generator")) != 3 {
		t.Errorf("generator calls = %d", len(h.agents.callsOf("component-generator")))
	}
}

func TestPickBestNodeChoosesWinner(t *testing.T) {
	h := setup(t)
	h.candidateScript()
	pid := h.candidatePipeline(true)
	id, w := h.start(h.admin, `{"is_test":true,"pipeline_id":"`+pid+`","input":{"brief":"popular button"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Error)
	}
	d := h.detail(id)
	if len(d.Results) != 1 || d.Results[0].ArtifactID == "" {
		t.Fatalf("results = %+v", d.Results)
	}
	if got := len(h.agents.callsOf("vision-judge")); got != 1 {
		t.Fatalf("judge calls = %d", got)
	}
	if len(stepsOf(d, "out")) != 1 {
		t.Errorf("output steps = %d", len(stepsOf(d, "out")))
	}
	if run.BestArtifactID == nil || *run.BestArtifactID != d.Results[0].ArtifactID {
		t.Errorf("best %v vs results %+v", run.BestArtifactID, d.Results)
	}
}
