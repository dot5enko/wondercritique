package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

func TestRunNodeArtifact(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("gen", ms.ID, "artifact", true)
	h.prov.Push(fake.Turn{
		Message: llm.NewMessage(llm.RoleAssistant, fake.Call("c1", "submit_artifact", map[string]string{"html": "<p class='x'>hi</p>"})),
		Usage:   llm.Usage{InputTokens: 120, OutputTokens: 30},
	})

	res, err := h.runtime.RunNode(context.Background(), NodeRequest{
		AgentID: def.ID, Samples: map[string]any{"brief": "a card"}, Owner: "user1", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HTML != "<p class='x'>hi</p>" || res.Contract.Type != "artifact" {
		t.Fatalf("result = %+v", res)
	}
	if res.Model() != "meta/muse-spark-1.3-contributor" || res.AgentSlug != "gen" || res.AgentVersion != 1 || res.ModelSetVersion != 1 {
		t.Errorf("meta = %s %s v%d ms v%d", res.Model(), res.AgentSlug, res.AgentVersion, res.ModelSetVersion)
	}
	if res.Usage.InputTokens != 120 || res.Steps != 1 || res.SessionID == "" {
		t.Errorf("usage/steps/session = %+v %d %q", res.Usage, res.Steps, res.SessionID)
	}
	// The input template was filled and the session persisted in the shared store.
	reqs := h.prov.Requests()
	if got := reqs[0].Messages[0].Text(); got != "Brief: a card" {
		t.Errorf("input = %q", got)
	}
	if _, err := h.runtime.agentStore.GetSession(context.Background(), res.SessionID); err != nil {
		t.Errorf("session not persisted: %v", err)
	}

	// Builds are cached: a second run reuses the agent.
	h.prov.Push(fake.ToolCalls(fake.Call("c2", "submit_artifact", map[string]string{"html": "<b>2</b>"})))
	if _, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "x"}}); err != nil {
		t.Fatal(err)
	}
	h.runtime.mu.Lock()
	n := len(h.runtime.nodeBuilds)
	h.runtime.mu.Unlock()
	if n != 1 {
		t.Errorf("node builds = %d, want 1", n)
	}
}

func TestRunNodeNudgesAnUnusableAnswerOnce(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("gen", ms.ID, "artifact", true)
	h.prov.Push(
		fake.Text("I think a blue card would be nice."), // no tool call, no HTML
		fake.ToolCalls(fake.Call("c1", "submit_artifact", map[string]string{"html": "<div>ok</div>"})),
	)
	res, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.HTML != "<div>ok</div>" || res.Steps != 2 {
		t.Fatalf("result = %+v", res)
	}
	reqs := h.prov.Requests()
	last := reqs[len(reqs)-1].Messages
	if nudge := last[len(last)-1].Text(); !strings.Contains(nudge, "could not be used") || !strings.Contains(nudge, "submit_artifact") {
		t.Errorf("nudge = %q", nudge)
	}

	// A second unusable answer fails the node.
	h.prov.Push(fake.Text("no html"), fake.Text("still nothing"))
	if _, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "x"}}); err == nil ||
		!strings.Contains(err.Error(), `agent "gen"`) {
		t.Errorf("err = %v", err)
	}
}

func TestRunNodeCritiqueAndCustomContracts(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	crit := h.seedAgent("crit", ms.ID, "critique", true)
	h.prov.Push(fake.ToolCalls(fake.Call("c1", "submit_critique", map[string]any{
		"strengths": []string{"bold"},
		"issues":    []map[string]string{{"severity": "ERROR", "message": "no contrast"}, {"severity": "weird", "message": "m"}},
		"score":     14,
	})))
	res, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: crit.ID, Samples: map[string]any{"brief": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Critique
	if c == nil || c.Score != MaxScore || len(c.Issues) != 2 || c.Issues[0].Severity != "error" || c.Issues[1].Severity != "warning" {
		t.Fatalf("critique = %+v", c)
	}

	// custom: the raw JSON object is kept.
	schema := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`)
	body := studio.AgentBody{ModelSetID: ms.ID, SystemPrompt: "s", InputTemplate: "{{brief}}",
		OutputContract: studio.OutputContract{Type: "custom", FieldsSchema: schema}}
	raw, _ := json.Marshal(body)
	cdef, err := h.store.Create(context.Background(), studio.KindAgent, studio.DefIdentity{Slug: "cust", Name: "cust"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.Publish(context.Background(), studio.KindAgent, cdef.ID, "", "t", raw); err != nil {
		t.Fatal(err)
	}
	h.prov.Push(fake.ToolCalls(fake.Call("c2", "submit_fields", map[string]string{"title": "Hello"})))
	res, err = h.runtime.RunNode(context.Background(), NodeRequest{AgentID: cdef.ID, Samples: map[string]any{"brief": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Fields), `"title":"Hello"`) {
		t.Errorf("fields = %s", res.Fields)
	}
}

func TestRunNodeResolvesPinnedPublishedAndDraftBodies(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("gen", ms.ID, "artifact", true) // v1: "You are a test agent."

	// v2 and then an unpublished draft with different system prompts.
	edit := func(system string) json.RawMessage {
		body := studio.AgentBody{ModelSetID: ms.ID, SystemPrompt: system, InputTemplate: "Brief: {{brief}}",
			Tools: []string{"submit_artifact"}, OutputContract: studio.OutputContract{Type: "artifact"}}
		raw, _ := json.Marshal(body)
		return raw
	}
	if _, _, err := h.store.Publish(context.Background(), studio.KindAgent, def.ID, "v2", "t", edit("SECOND")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutDraft(context.Background(), studio.KindAgent, def.ID, edit("DRAFT")); err != nil {
		t.Fatal(err)
	}

	systemOf := func(req NodeRequest) (string, *NodeResult) {
		h.prov.Push(fake.ToolCalls(fake.Call("c", "submit_artifact", map[string]string{"html": "<i>x</i>"})))
		n := len(h.prov.Requests())
		res, err := h.runtime.RunNode(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return h.prov.Requests()[n].System, res
	}
	samples := map[string]any{"brief": "b"}

	if sys, res := systemOf(NodeRequest{AgentID: def.ID, Samples: samples}); !strings.Contains(sys, "SECOND") || res.AgentVersion != 2 {
		t.Errorf("latest: system=%q v%d", sys, res.AgentVersion)
	}
	if sys, res := systemOf(NodeRequest{AgentID: def.ID, Version: 1, Samples: samples}); !strings.Contains(sys, "You are a test agent.") || res.AgentVersion != 1 {
		t.Errorf("pinned v1: system=%q v%d", sys, res.AgentVersion)
	}
	if sys, res := systemOf(NodeRequest{AgentID: def.ID, Draft: true, Samples: samples}); !strings.Contains(sys, "DRAFT") || res.AgentVersion != 0 {
		t.Errorf("draft: system=%q v%d", sys, res.AgentVersion)
	}
	// Draft builds are not cached and drop their material afterwards.
	h.runtime.mu.Lock()
	cached := len(h.runtime.nodeBuilds)
	h.runtime.mu.Unlock()
	if cached != 2 { // latest + pinned v1
		t.Errorf("cached node builds = %d, want 2", cached)
	}
	h.runtime.materialMu.RLock()
	for name := range h.runtime.material {
		if strings.Contains(name, "@draft-") {
			t.Errorf("draft material %q leaked", name)
		}
	}
	h.runtime.materialMu.RUnlock()
}

func TestRunNodeErrors(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	unpublished := h.seedAgent("wip", ms.ID, "artifact", false)
	// Unpublished agents have a draft only: published resolution fails, draft works.
	if _, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: unpublished.ID}); err == nil {
		t.Error("unpublished agent resolved without Draft")
	}
	if _, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: "missing"}); err == nil {
		t.Error("unknown agent resolved")
	}
	// Provider failures surface (the fake provider has no turns left).
	def := h.seedAgent("gen", ms.ID, "artifact", true)
	if _, err := h.runtime.RunNode(context.Background(), NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "x"}}); err == nil {
		t.Error("exhausted provider did not fail the node")
	}
}

func TestParsers(t *testing.T) {
	c, err := ParseCritique("Here you go:\n```json\n{\"score\": 7.5, \"strengths\": [\"a\"], \"issues\": []}\n```")
	if err != nil || c.Score != 7.5 || len(c.Issues) != 0 || len(c.Strengths) != 1 {
		t.Errorf("fenced critique = %+v, %v", c, err)
	}
	if _, err := ParseCritique(`{"strengths":[]}`); err == nil || !strings.Contains(err.Error(), "no score") {
		t.Errorf("missing score err = %v", err)
	}
	if _, err := ParseCritique("great work"); err == nil {
		t.Error("prose parsed as critique")
	}
	if c, _ := ParseCritique(`{"score": -3}`); c == nil || c.Score != 0 {
		t.Errorf("negative score = %+v", c)
	}

	if html, err := ExtractHTML("```html\n<div>x</div>\n```"); err != nil || html != "<div>x</div>" {
		t.Errorf("fenced html = %q, %v", html, err)
	}
	if html, err := ExtractHTML("  <p>raw</p>  "); err != nil || html != "<p>raw</p>" {
		t.Errorf("raw html = %q, %v", html, err)
	}
	for _, bad := range []string{"", "   ", "just words"} {
		if _, err := ExtractHTML(bad); err == nil {
			t.Errorf("ExtractHTML(%q) succeeded", bad)
		}
	}
}

func TestBuildInputImageLists(t *testing.T) {
	imgs := []string{"wcblob://" + strings.Repeat("a", 64), "wcblob://" + strings.Repeat("b", 64)}
	msg := BuildInput("Shots: {{images}} / {{brief}}", map[string]any{"images": imgs, "brief": "go"}, false)
	if msg.Text() != "Shots:  / go" {
		t.Errorf("text = %q", msg.Text())
	}
	if len(msg.Blocks) != 3 || msg.Blocks[1].ImageURL != imgs[0] || msg.Blocks[2].ImageURL != imgs[1] {
		t.Errorf("blocks = %+v", msg.Blocks)
	}
	// Preview shows a placeholder and no blocks; non-image lists stay text.
	prev := BuildInput("{{images}}", map[string]any{"images": imgs}, true)
	if prev.Text() != "[image: images]" || len(prev.Blocks) != 1 {
		t.Errorf("preview = %q %d", prev.Text(), len(prev.Blocks))
	}
	txt := BuildInput("{{input}}", map[string]any{"input": []string{"a", "b"}}, false)
	if txt.Text() != `["a","b"]` {
		t.Errorf("text list = %q", txt.Text())
	}
}

func TestRunNodeMountsTheThreadCanvasAndParsesDestHints(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	if err := h.db.AutoMigrate(&models.Run{}, &models.Artifact{}, &models.CanvasFile{}, &models.CanvasState{}); err != nil {
		t.Fatal(err)
	}
	bl := blobs.NewStore(h.db)
	cv := canvas.NewStore(h.db, bl)
	h.runtime.canvas = cv
	sha, _ := bl.Put(ctx, []byte("<div>tiers</div>"), "text/html; charset=utf-8")
	art := models.Artifact{Base: models.Base{ID: models.NewID()}, RunID: "r1", Kind: models.ArtifactHTML, BlobSHA: sha}
	if err := h.db.Create(&art).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := cv.Commit(ctx, "thread1", "r1", canvas.Dest{Folder: "/pricing"}, []canvas.CommitItem{{Name: "tiers.html", ArtifactID: art.ID}}); err != nil {
		t.Fatal(err)
	}

	ms := h.seedModelSet()
	def := h.seedAgent("gen", ms.ID, "artifact", true)
	h.prov.Push(fake.ToolCalls(fake.Call("c1", "submit_artifact", map[string]any{
		"html": "<div class='dark'>tiers</div>", "folder": "/pricing/dark", "names": []string{"tiers.html"},
	})))
	res, err := h.runtime.RunNode(ctx, NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "dark"}, Owner: "user1", ThreadID: "thread1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HTML != "<div class='dark'>tiers</div>" || len(res.Items) != 1 || res.Folder != "/pricing/dark" || len(res.Names) != 1 || res.Names[0] != "tiers.html" {
		t.Fatalf("result = %+v", res)
	}

	// The session records the thread and mounts its canvas read-only.
	sess, err := h.runtime.agentStore.GetSession(ctx, res.SessionID)
	if err != nil || sess.Metadata["thread_id"] != "thread1" {
		t.Fatalf("session metadata = %v, %v", sess, err)
	}
	mnt, ok, err := h.runtime.sessionMount(ctx, res.SessionID)
	if err != nil || !ok {
		t.Fatalf("mount: %v %v", ok, err)
	}
	if raw, err := mnt.Read(ctx, "/canvas/pricing/tiers.html"); err != nil || string(raw) != "<div>tiers</div>" {
		t.Errorf("read canvas = %q, %v", raw, err)
	}
	if err := mnt.Write(ctx, "/canvas/pricing/x.html", []byte("x")); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("write canvas: %v", err)
	}
	if err := mnt.Write(ctx, "/work/x.html", []byte("x")); err != nil {
		t.Errorf("write work: %v", err)
	}

	// Without a thread there is no /canvas.
	h.prov.Push(fake.ToolCalls(fake.Call("c2", "submit_artifact", map[string]string{"html": "<b>x</b>"})))
	res, err = h.runtime.RunNode(ctx, NodeRequest{AgentID: def.ID, Samples: map[string]any{"brief": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "" || res.Items != nil {
		t.Errorf("plain submit = %+v", res)
	}
	mnt, _, _ = h.runtime.sessionMount(ctx, res.SessionID)
	if _, err := mnt.Stat(ctx, "/canvas"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("canvas without thread: %v", err)
	}
}

func TestParseComponentSetEnvelope(t *testing.T) {
	for in, want := range map[string]int{
		`{"components":["<a>1</a>","<a>2</a>"]}`:         2,
		`{"components":["<a>1</a>"]}`:                    0, // a single item needs hints
		`{"components":["<a>1</a>"],"folder":"/x"}`:      1,
		`{"components":["<a>1</a>"],"names":["a.html"]}`: 1,
		`{"components":[],"folder":"/x"}`:                0,
		`{"html":"<a>"}`:                                 0,
		`<div>{"components":["x","y"]}</div>`:            0,
	} {
		set, ok := parseComponentSet(in)
		if got := len(set.Components); (want == 0 && ok) || (want > 0 && (!ok || got != want)) {
			t.Errorf("parseComponentSet(%s) = %+v %v", in, set, ok)
		}
	}
}
