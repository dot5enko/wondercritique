package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	featllm "github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/go-chi/chi/v5"
)

const testIssuer = "wondercritique-test"

type harness struct {
	t       *testing.T
	router  chi.Router
	store   *studio.Store
	runtime *Runtime
	headers string
	prov    *fake.Provider
}

func setup(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	db, err := dbx.Open(fmt.Sprintf("file:agentrun_%s?mode=memory&cache=shared", models.NewID()), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.StudioDef{}, &models.StudioDefVersion{}, &blobs.Blob{}); err != nil {
		t.Fatal(err)
	}
	defs := studio.NewStore(db)
	cfg := &config.Config{Env: "dev", JWTSecret: strings.Repeat("j", 40), SecretsKey: strings.Repeat("s", 40)}
	svc, err := featllm.NewService(db, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	agentStore := memory.New()
	rt, err := NewRuntime(defs, svc, agentStore, db, blobs.NewStore(db), log)
	if err != nil {
		t.Fatal(err)
	}
	prov := fake.New()
	rt.providerFn = func(ctx context.Context, providerID string) (llm.Provider, error) {
		if providerID == "meta" {
			return prov, nil
		}
		return nil, apperr.Unavailable("provider is not enabled")
	}

	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", testIssuer)
	r := chi.NewRouter()
	ctrl := NewController(rt, defs, mw)
	ctrl.Routes(r)
	// Mirrors the studio controller's /studio subrouter (admin middleware + extenders).
	r.Route("/studio", func(r chi.Router) {
		r.Use(mw.Required, auth.Role("admin"))
		ctrl.StudioRoutes(r)
	})

	// admin token
	token, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: "admin1"}, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, router: r, store: defs, runtime: rt, headers: "Bearer " + token, prov: prov}
}

func (h *harness) seedModelSet() *models.StudioDef {
	h.t.Helper()
	body := studio.ModelSetBody{Strategy: "single", Entries: []studio.ModelSetEntry{{ProviderID: "meta", ModelID: "muse-spark-1.3-contributor"}}}
	raw, _ := json.Marshal(body)
	def, err := h.store.Create(context.Background(), studio.KindModelSet, studio.DefIdentity{Slug: "main", Name: "Main"}, raw)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, _, err := h.store.Publish(context.Background(), studio.KindModelSet, def.ID, "", "test", raw); err != nil {
		h.t.Fatal(err)
	}
	return def
}

// seedAgent creates (and optionally publishes) an agent def. withDraft=false
// creates the def without an initial draft.
func (h *harness) seedAgent(slug, modelSetID string, contract string, publish bool) *models.StudioDef {
	h.t.Helper()
	body := studio.AgentBody{
		ModelSetID:     modelSetID,
		SystemPrompt:   "You are a test agent.",
		InputTemplate:  "Brief: {{brief}}",
		Tools:          []string{"submit_artifact"},
		OutputContract: studio.OutputContract{Type: contract},
	}
	raw, _ := json.Marshal(body)
	def, err := h.store.Create(context.Background(), studio.KindAgent, studio.DefIdentity{Slug: slug, Name: slug}, raw)
	if err != nil {
		h.t.Fatal(err)
	}
	if publish {
		if _, _, err := h.store.Publish(context.Background(), studio.KindAgent, def.ID, "", "test", raw); err != nil {
			h.t.Fatal(err)
		}
	}
	return def
}

func (h *harness) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	hd := h.headers
	if len(headers) > 0 {
		hd = headers[0]
	}
	req.Header.Set("Authorization", hd)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

/* ---------- prompt assembly ---------- */

func TestAssembleSystemGolden(t *testing.T) {
	system := AssembleSystem("You are a generator.",
		[]studio.RuleGroup{
			{Name: "Style", Rules: []studio.RuleItem{{Text: "Be tidy.", Severity: "error"}}},
		},
		[]PromptSkill{{Slug: "html-basics", Manifest: "# HTML basics\n\nUse semantic tags.", Files: 2}},
		studio.OutputContract{Type: "artifact"},
	)
	want := `You are a generator.

## Rules

### Style
- Be tidy.


## Skills

### html-basics

# HTML basics

Use semantic tags.

2 additional file(s) of this skill are mounted read-only at /skills/html-basics/.


## Output

Produce one complete HTML component fragment as your answer and call submit_artifact with it to finish. The fragment must pass the platform rules (no scripts, one root element, scoped CSS).`
	if system != want {
		t.Errorf("system =\n%s\n\nwant\n%s", system, want)
	}

	// Empty sections are omitted.
	minimal := AssembleSystem("", nil, nil, studio.OutputContract{})
	if minimal != "" {
		t.Errorf("minimal = %q", minimal)
	}

	critique := AssembleSystem("s", nil, nil, studio.OutputContract{Type: "critique"})
	if !strings.Contains(critique, "submit_critique") || !strings.Contains(critique, "## Output") {
		t.Errorf("critique output section = %s", critique)
	}
}

func TestBuildInput(t *testing.T) {
	// Template substitution; images become blocks after the text; missing
	// known variables render as ""; unknown placeholders stay verbatim.
	msg := BuildInput("Brief: {{brief}}\nShot: {{images}}\nWeird: {{not-a-var}}", map[string]any{
		"brief":  "make a card",
		"images": "data:image/png;base64,AAA",
	}, false)
	if msg.Text() != "Brief: make a card\nShot: \nWeird: {{not-a-var}}" {
		t.Errorf("text = %q", msg.Text())
	}
	if len(msg.Blocks) != 2 || msg.Blocks[1].Type != llm.BlockImage || msg.Blocks[1].ImageURL != "data:image/png;base64,AAA" {
		t.Errorf("blocks = %+v", msg.Blocks)
	}

	// Preview: images render as placeholders.
	pv := BuildInput("Shot: {{images}}", map[string]any{"images": "wcblob://abc"}, true)
	if pv.Text() != "Shot: [image: images]" || len(pv.Blocks) != 1 {
		t.Errorf("preview = %q %+v", pv.Text(), pv.Blocks)
	}

	// Empty template: markdown sections.
	sec := BuildInput("", map[string]any{"brief": "b", "html": "<div/>"}, false)
	if sec.Text() != "## brief\n\nb\n\n## html\n\n<div/>" {
		t.Errorf("sections = %q", sec.Text())
	}
}

/* ---------- body resolution ---------- */

func TestResolveAgentDraftVsPublished(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	ctx := context.Background()

	// A def without draft and published version → 404.
	empty, err := h.store.Create(ctx, studio.KindAgent, studio.DefIdentity{Slug: "empty", Name: "Empty"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.runtime.resolveAgent(ctx, empty.ID, true); err == nil {
		t.Fatal("expected 404")
	}

	def := h.seedAgent("agent-x", ms.ID, "artifact", false)

	// Draft → used.
	draft := studio.AgentBody{ModelSetID: ms.ID, SystemPrompt: "DRAFT SYSTEM", InputTemplate: "{{brief}}", OutputContract: studio.OutputContract{Type: "critique"}}
	raw, _ := json.Marshal(draft)
	if _, err := h.store.PutDraft(ctx, studio.KindAgent, def.ID, raw); err != nil {
		t.Fatal(err)
	}
	res, err := h.runtime.resolveAgent(ctx, def.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Body.SystemPrompt != "DRAFT SYSTEM" {
		t.Errorf("draft not used: %+v", res.Body)
	}

	// Publish a different body; draftFirst=false must use the published one.
	pub := draft
	pub.SystemPrompt = "PUBLISHED SYSTEM"
	published, _ := json.Marshal(pub)
	if _, _, err := h.store.Publish(ctx, studio.KindAgent, def.ID, "", "test", published); err != nil {
		t.Fatal(err)
	}
	res, err = h.runtime.resolveAgent(ctx, def.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Body.SystemPrompt != "PUBLISHED SYSTEM" {
		t.Errorf("published not used: %+v", res.Body)
	}
	// draftFirst=true prefers the (new) draft over the published version.
	fresh := pub
	fresh.SystemPrompt = "FRESH DRAFT"
	freshRaw, _ := json.Marshal(fresh)
	if _, err := h.store.PutDraft(ctx, studio.KindAgent, def.ID, freshRaw); err != nil {
		t.Fatal(err)
	}
	res, _ = h.runtime.resolveAgent(ctx, def.ID, true)
	if res.Body.SystemPrompt != "FRESH DRAFT" {
		t.Errorf("draft-first not used: %+v", res.Body)
	}
}

/* ---------- agent build + terminal submit ---------- */

func TestAgentBuildTerminalSubmitEndsRun(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("gen", ms.ID, "artifact", true)
	res, err := h.runtime.resolveAgent(context.Background(), def.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	h.prov.Push(fake.ToolCalls(fake.Call("c1", "submit_artifact", map[string]string{"html": "<div class='card'>hi</div>"})))

	agent, err := h.runtime.buildAgent(context.Background(), res, "t:"+def.ID, memory.New())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := agent.NewSession(context.Background(), agentkit.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := agent.RunSync(context.Background(), sess.ID, llm.UserText("make a thing"))
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunCompleted {
		t.Fatalf("status = %v", run.Status)
	}
	if run.Text != "<div class='card'>hi</div>" {
		t.Errorf("text = %q", run.Text)
	}
	if h.prov.Remaining() != 0 {
		t.Errorf("turns left = %d", h.prov.Remaining())
	}
	// The assembled system prompt carries the output contract section.
	reqs := h.prov.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].System, "submit_artifact") {
		t.Fatalf("requests = %+v", reqs)
	}
}

/* ---------- preview ---------- */

func TestPreview(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("prev", ms.ID, "artifact", true)

	w := h.do(http.MethodPost, "/studio/agents/"+def.ID+"/preview", `{"input":{"brief":"build a chart"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	var out struct {
		System string `json:"system"`
		Input  string `json:"input"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.System, "You are a test agent.") || !strings.Contains(out.System, "submit_artifact") {
		t.Errorf("system = %q", out.System)
	}
	if out.Input != "Brief: build a chart" {
		t.Errorf("input = %q", out.Input)
	}
	// Unknown id → 404.
	if w := h.do(http.MethodPost, "/studio/agents/nope/preview", `{"input":{}}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown preview = %d", w.Code)
	}
}

/* ---------- try (SSE) ---------- */

func TestTrySSE(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	def := h.seedAgent("tryagent", ms.ID, "artifact", true)
	h.prov.Push(fake.ToolCalls(fake.Call("c1", "submit_artifact", map[string]string{"html": "<b>done</b>"})))

	w := h.do(http.MethodPost, "/studio/agents/"+def.ID+"/try", `{"input":{"brief":"go"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("try = %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"event: run_started", "event: tool_call_started", "event: run_finished", "event: done\ndata: {}\n\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	unescaped := strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&").Replace(body)
	if !strings.Contains(body, `"status":"completed"`) || !strings.Contains(unescaped, "<b>done</b>") {
		t.Errorf("final text not in stream: %s", body)
	}
}

/* ---------- chat mount ---------- */

func TestChatMount404And503(t *testing.T) {
	h := setup(t)
	// Unknown slug → 404.
	if w := h.do(http.MethodPost, "/agents/ghost/sessions", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown slug = %d %s", w.Code, w.Body)
	}
	// Unpublished agent → 404.
	ms := h.seedModelSet()
	h.seedAgent("draft-only", ms.ID, "artifact", false)
	if w := h.do(http.MethodPost, "/agents/draft-only/sessions", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("unpublished = %d %s", w.Code, w.Body)
	}
	// Published agent, provider unavailable → 503.
	h2 := setup(t)
	ms2 := h2.seedModelSet()
	h2.seedAgent("broken", ms2.ID, "artifact", true)
	h2.runtime.providerFn = func(ctx context.Context, providerID string) (llm.Provider, error) {
		return nil, apperr.Unavailable("provider is not enabled")
	}
	if w := h2.do(http.MethodPost, "/agents/broken/sessions", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d %s", w.Code, w.Body)
	}
	// No auth → 401.
	if w := h.do(http.MethodPost, "/agents/ghost/sessions", `{}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth = %d", w.Code)
	}
}

func TestChatMount200(t *testing.T) {
	h := setup(t)
	ms := h.seedModelSet()
	h.seedAgent("critic", ms.ID, "critique", true)
	h.prov.Push(fake.ToolCalls(fake.Call("c1", "submit_critique", map[string]any{
		"strengths": []string{"clear"}, "issues": []any{}, "score": 8,
	})))

	// Create a session via the mounted httpapi.
	w := h.do(http.MethodPost, "/agents/critic/sessions", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create session = %d %s", w.Code, w.Body)
	}
	var sess struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	// Run synchronously (stream=false) and read the critique.
	w = h.do(http.MethodPost, "/agents/critic/sessions/"+sess.ID+"/runs?stream=false", `{"input":"review this"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("run = %d %s", w.Code, w.Body)
	}
	var out struct {
		Text   string `json:"text"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" || !strings.Contains(out.Text, `"score":8`) {
		t.Fatalf("run = %+v", out)
	}
}

/* ---------- pipeline validate ---------- */

func TestValidatePipelineEndpoint(t *testing.T) {
	h := setup(t)
	body := studio.PipelineBody{
		Nodes: []studio.PipelineNode{
			{ID: "in", Type: "input"},
			{ID: "out", Type: "output"},
		},
		Edges: []studio.PipelineEdge{
			{ID: "e", From: studio.EdgeEndpoint{Node: "in", Port: "brief"}, To: studio.EdgeEndpoint{Node: "out", Port: "html"}},
		},
	}
	raw, _ := json.Marshal(body)
	def, err := h.store.Create(context.Background(), studio.KindPipeline, studio.DefIdentity{Slug: "p", Name: "P"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	w := h.do(http.MethodPost, "/studio/pipelines/"+def.ID+"/validate", "")
	if w.Code != http.StatusOK {
		t.Fatalf("validate = %d %s", w.Code, w.Body)
	}
	var out struct {
		OK     bool                   `json:"ok"`
		Issues []studio.PipelineIssue `json:"issues"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || len(out.Issues) != 0 {
		t.Fatalf("result = %+v", out)
	}

	// Broken graph: unknown target node.
	body.Edges[0].To.Node = "ghost"
	raw, _ = json.Marshal(body)
	def2, err := h.store.Create(context.Background(), studio.KindPipeline, studio.DefIdentity{Slug: "p2", Name: "P"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	w = h.do(http.MethodPost, "/studio/pipelines/"+def2.ID+"/validate", "")
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.OK || len(out.Issues) == 0 {
		t.Fatalf("broken result = %+v", out)
	}
}
