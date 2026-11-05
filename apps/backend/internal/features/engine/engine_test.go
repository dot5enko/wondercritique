package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	llmsvc "github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

/* ---------- fakes ---------- */

// fakeAgents answers node runs from a per-slug script.
type fakeAgents struct {
	mu      sync.Mutex
	slugs   map[string]string // agent def id -> slug
	calls   map[string][]agentrun.NodeRequest
	handler func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error)
	// ctxHandler, when set, wins over handler (agents that use the step's sink).
	ctxHandler func(ctx context.Context, slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error)
}

func (f *fakeAgents) RunNode(ctx context.Context, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
	f.mu.Lock()
	slug := f.slugs[req.AgentID]
	f.calls[slug] = append(f.calls[slug], req)
	n := len(f.calls[slug])
	f.mu.Unlock()
	if f.ctxHandler != nil {
		return f.ctxHandler(ctx, slug, n, req)
	}
	return f.handler(slug, n, req)
}

func (f *fakeAgents) callsOf(slug string) []agentrun.NodeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentrun.NodeRequest(nil), f.calls[slug]...)
}

func htmlResult(html string) *agentrun.NodeResult {
	return &agentrun.NodeResult{
		Contract: studio.OutputContract{Type: "artifact"}, HTML: html, Text: html, SessionID: "sess-gen",
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}, ProviderID: "meta", ModelID: "muse",
		AgentVersion: 1, ModelSetVersion: 1,
	}
}

func critiqueResult(score float64, issue string) *agentrun.NodeResult {
	c := &agentrun.Critique{Strengths: []string{"clean"}, Score: score}
	if issue != "" {
		c.Issues = []agentrun.CritiqueIssue{{Severity: "warning", Code: "spacing", Message: issue, Suggestion: "add padding"}}
	}
	return &agentrun.NodeResult{
		Contract: studio.OutputContract{Type: "critique"}, Critique: c, SessionID: "sess-crit",
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}, ProviderID: "meta", ModelID: "muse", AgentVersion: 1,
	}
}

type fakeRenderer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeRenderer) Render(ctx context.Context, req render.Request) (*render.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var shots []render.Shot
	variants := req.Variants
	if len(variants) == 0 {
		variants = []render.Variant{render.DefaultVariant()}
	}
	for _, v := range variants {
		shots = append(shots, render.Shot{Variant: v, PNG: []byte("\x89PNG-" + req.HTML), Width: v.Width, Height: 300})
	}
	return &render.Result{CanvasVersion: render.CanvasVersion, Shots: shots}, nil
}

type fakeCosts struct{}

func (fakeCosts) ModelCost(provider, model string) *llmsvc.ModelCost {
	return &llmsvc.ModelCost{Input: 1, Output: 2}
}

/* ---------- harness ---------- */

type harness struct {
	t        *testing.T
	e        *Engine
	agents   *fakeAgents
	renderer *fakeRenderer
	defs     *studio.Store
	router   chi.Router
	mw       *auth.Middleware
	admin    string // bearer token
	user     string
	other    string
}

func setup(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	// A WAL file like production: parallel nodes write concurrently, which a
	// shared-cache in-memory database would reject with SQLITE_LOCKED.
	db, err := dbx.Open(filepath.Join(t.TempDir(), "engine.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.StudioDef{}, &models.StudioDefVersion{}, &blobs.Blob{},
		&models.Run{}, &models.RunStep{}, &models.Artifact{}, &models.ResultArtifact{},
		&models.CanvasFile{}, &models.CanvasState{}, &models.Styleguide{}); err != nil {
		t.Fatal(err)
	}
	defs := studio.NewStore(db)
	if err := studio.SeedDefaults(context.Background(), defs, log); err != nil {
		t.Fatal(err)
	}

	agents := &fakeAgents{slugs: map[string]string{}, calls: map[string][]agentrun.NodeRequest{}}
	list, err := defs.List(context.Background(), studio.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		agents.slugs[d.ID] = d.Slug
	}
	rend := &fakeRenderer{}
	bl := blobs.NewStore(db)
	e := New(Deps{DB: db, Defs: defs, Agents: agents, Renderer: rend, Blobs: bl, Canvas: canvas.NewStore(db, bl), Costs: fakeCosts{}, Log: log, Concurrency: 2})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})

	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", "engine-test")
	token := func(id, role string) string {
		tok, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: id}, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + tok
	}
	r := chi.NewRouter()
	NewController(e, mw).Routes(r)
	return &harness{t: t, e: e, agents: agents, renderer: rend, defs: defs, router: r, mw: mw,
		admin: token("admin1", "admin"), user: token("user1", "user"), other: token("user2", "user")}
}

func (h *harness) do(method, path, body, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// waitStatus waits for the run to leave pending/running.
func (h *harness) waitDone(id string) *models.Run {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var run models.Run
		if err := h.e.DB.First(&run, "id = ?", id).Error; err != nil {
			h.t.Fatal(err)
		}
		if run.Status != models.RunPending && run.Status != models.RunRunning {
			return &run
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("run %s did not finish", id)
	return nil
}

func (h *harness) start(token, body string) (string, *httptest.ResponseRecorder) {
	h.t.Helper()
	w := h.do("POST", "/runs", body, token)
	if w.Code != http.StatusCreated {
		return "", w
	}
	var out struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.ID, w
}

func (h *harness) detail(id string) *RunDetail {
	h.t.Helper()
	d, err := h.e.RunDetail(context.Background(), id, Viewer{Admin: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func stepsOf(d *RunDetail, node string) []StepView {
	var out []StepView
	for _, s := range d.Steps {
		if s.NodeID == node {
			out = append(out, s)
		}
	}
	return out
}

// scripted installs the standard generator/critic script: gen returns v<n>,
// vision and code critics score from the given per-iteration lists.
func (h *harness) scripted(vision, code []float64) {
	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		switch slug {
		case "component-generator":
			return htmlResult(fmt.Sprintf("<div class='v%d'>design %d</div>", n, n)), nil
		case "vision-critic":
			return critiqueResult(vision[min(n, len(vision))-1], "tighten the hero spacing"), nil
		case "code-critic":
			return critiqueResult(code[min(n, len(code))-1], "use semantic tags"), nil
		}
		return nil, fmt.Errorf("unexpected agent %q", slug)
	}
}

/* ---------- pipeline runs ---------- */

func TestDesignLoopIteratesUntilScoreReached(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{5, 9}, []float64{6, 9})

	id, w := h.start(h.user, `{"input":{"brief":"A pricing card"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCompleted {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	d := h.detail(id)

	// iteration 1: no output (gate continued), iteration 2: output ran.
	if got := len(stepsOf(d, "generator")); got != 2 {
		t.Fatalf("generator steps = %d, want 2", got)
	}
	if got := len(stepsOf(d, "output")); got != 1 {
		t.Fatalf("output steps = %d, want 1", got)
	}
	if out := stepsOf(d, "output")[0]; out.Iteration != 2 {
		t.Errorf("output ran in iteration %d, want 2", out.Iteration)
	}
	if run.Iteration != 2 {
		t.Errorf("iteration = %d", run.Iteration)
	}
	if run.BestArtifactID == nil {
		t.Fatal("no best artifact")
	}

	// Best artifact: the iteration-2 HTML, carrying the merged score 9.
	var best *ArtifactView
	for i := range d.Artifacts {
		if d.Artifacts[i].ID == *run.BestArtifactID {
			best = &d.Artifacts[i]
		}
	}
	if best == nil || best.Kind != "html" || best.Iteration != 2 || best.Score == nil || *best.Score != 9 {
		t.Fatalf("best artifact = %+v", best)
	}
	blob, err := h.e.ArtifactBlob(context.Background(), best.ID, Viewer{Admin: true})
	if err != nil || !strings.Contains(string(blob.Data), "design 2") {
		t.Fatalf("best content = %q, %v", blob.Data, err)
	}
	if run.BestScore == nil || *run.BestScore != 9 {
		t.Errorf("best score = %v", run.BestScore)
	}

	// Iteration 1's html carries score 5.5; renders/ shots per iteration.
	var iter1HTML *ArtifactView
	shots := 0
	for i, a := range d.Artifacts {
		if a.Kind == "html" && a.Iteration == 1 {
			iter1HTML = &d.Artifacts[i]
		}
		if a.Kind == "image" {
			shots++
		}
	}
	if iter1HTML == nil || iter1HTML.Score == nil || *iter1HTML.Score != 5.5 {
		t.Errorf("iteration 1 html = %+v", iter1HTML)
	}
	if shots != 2 || h.renderer.calls != 2 {
		t.Errorf("shots = %d, render calls = %d", shots, h.renderer.calls)
	}

	// The generator saw the feedback (critique + previous HTML) only in iteration 2.
	gen := h.agents.callsOf("component-generator")
	if _, ok := gen[0].Samples["feedback"]; ok {
		t.Errorf("iteration 1 feedback = %v, want none", gen[0].Samples["feedback"])
	}
	fb, _ := gen[1].Samples["feedback"].(string)
	for _, want := range []string{"Feedback on your previous attempt (iteration 1)", "tighten the hero spacing", "design 1", "Score: 5.5/10"} {
		if !strings.Contains(fb, want) {
			t.Errorf("feedback lacks %q:\n%s", want, fb)
		}
	}
	// The vision critic received the screenshots as a list and the brief from the run input.
	vis := h.agents.callsOf("vision-critic")[0]
	if imgs, ok := vis.Samples["images"].([]string); !ok || len(imgs) != 1 || !strings.HasPrefix(imgs[0], "wcblob://") {
		t.Errorf("vision images = %#v", vis.Samples["images"])
	}
	if vis.Samples["brief"] != "A pricing card" {
		t.Errorf("vision brief = %v", vis.Samples["brief"])
	}

	// Usage/cost roll up (8+7 step... agent steps only carry usage).
	if run.Usage.InputTokens == 0 || run.Cost == nil || *run.Cost <= 0 {
		t.Errorf("usage = %+v cost = %v", run.Usage, run.Cost)
	}
	gs := stepsOf(d, "generator")[0]
	if gs.Model != "meta/muse" || gs.PinnedVersions["agent"] != 1 || gs.AgentSessionID != "sess-gen" || len(gs.ArtifactIDs) != 1 {
		t.Errorf("generator step = %+v", gs)
	}
	// The gate explains its decision.
	var gate struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	_ = json.Unmarshal(stepsOf(d, "gate")[0].Output, &gate)
	if gate.Decision != "continue" || !strings.Contains(gate.Reason, "below the target") {
		t.Errorf("gate 1 = %+v", gate)
	}
	_ = json.Unmarshal(stepsOf(d, "gate")[1].Output, &gate)
	if gate.Decision != "done" {
		t.Errorf("gate 2 = %+v", gate)
	}
}

func TestGateStopsWhenScoreDoesNotImprove(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{4, 4, 9}, []float64{4, 4, 9})

	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	run := h.waitDone(id)
	if run.Status != models.RunCompleted {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	if run.Iteration != 2 {
		t.Fatalf("iterations = %d, want 2 (patience 1 stops at the first non-improvement)", run.Iteration)
	}
	// Tie on score: the later iteration wins.
	d := h.detail(id)
	var best ArtifactView
	for _, a := range d.Artifacts {
		if a.ID == *run.BestArtifactID {
			best = a
		}
	}
	if best.Iteration != 2 {
		t.Errorf("best iteration = %d", best.Iteration)
	}
}

func TestGateStopsAtMaxIterationsAndPicksBest(t *testing.T) {
	h := setup(t)
	// Improving each time but never reaching 8 → stops at max_iterations (3);
	// then a regression in a longer script would not matter.
	h.scripted([]float64{2, 4, 6}, []float64{2, 4, 6})
	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	run := h.waitDone(id)
	if run.Status != models.RunCompleted || run.Iteration != 3 {
		t.Fatalf("status=%s iteration=%d err=%s", run.Status, run.Iteration, run.Error)
	}
	if run.BestScore == nil || *run.BestScore != 6 {
		t.Errorf("best score = %v", run.BestScore)
	}
}

func TestBestStrategyPicksEarlierIteration(t *testing.T) {
	h := setup(t)
	// Iteration 1 scores 7, iteration 2 regresses to 5 → patience stops; the
	// output picks iteration 1 (higher score).
	h.scripted([]float64{7, 5}, []float64{7, 5})
	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	run := h.waitDone(id)
	if run.Status != models.RunCompleted || run.Iteration != 2 {
		t.Fatalf("status=%s iteration=%d err=%s", run.Status, run.Iteration, run.Error)
	}
	d := h.detail(id)
	var best ArtifactView
	for _, a := range d.Artifacts {
		if a.ID == *run.BestArtifactID {
			best = a
		}
	}
	if best.Iteration != 1 || best.Score == nil || *best.Score != 7 {
		t.Errorf("best = %+v, want iteration 1 score 7", best)
	}
	if run.BestScore == nil || *run.BestScore != 7 {
		t.Errorf("best score = %v", run.BestScore)
	}
}

func TestAgentFailureFailsTheRun(t *testing.T) {
	h := setup(t)
	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		if slug == "component-generator" {
			return nil, fmt.Errorf("provider %q is not configured", "meta")
		}
		return nil, fmt.Errorf("unexpected agent")
	}
	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	run := h.waitDone(id)
	if run.Status != models.RunFailed || !strings.Contains(run.Error, "Generator") || !strings.Contains(run.Error, "not configured") {
		t.Fatalf("status=%s error=%q", run.Status, run.Error)
	}
	d := h.detail(id)
	if s := stepsOf(d, "generator"); len(s) != 1 || s[0].Status != models.RunFailed || s[0].Error == "" {
		t.Errorf("generator steps = %+v", s)
	}
	if len(stepsOf(d, "render")) != 0 {
		t.Error("render ran after the generator failed")
	}
	if got := h.canvasPaths(id); len(got) != 0 {
		t.Errorf("a failed run committed %v", got)
	}
}

func TestRenderUnavailableGivesReadableError(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	h.renderer.err = render.ErrNoBrowser
	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	run := h.waitDone(id)
	if run.Status != models.RunFailed || !strings.Contains(run.Error, "no Chrome/Chromium") {
		t.Fatalf("status=%s error=%q", run.Status, run.Error)
	}
}

func TestCancelRun(t *testing.T) {
	h := setup(t)
	started := make(chan struct{}, 4)
	h.e.Agents = blockingAgents{started: started}

	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	<-started
	if w := h.do("POST", "/runs/"+id+"/cancel", "", h.user); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCancelled {
		t.Fatalf("status = %s (%s)", run.Status, run.Error)
	}
	d := h.detail(id)
	if s := stepsOf(d, "generator"); len(s) != 1 || s[0].Status != models.RunCancelled {
		t.Errorf("generator steps = %+v", s)
	}
	// A second cancel on a finished run conflicts.
	if w := h.do("POST", "/runs/"+id+"/cancel", "", h.user); w.Code != http.StatusConflict {
		t.Errorf("second cancel: %d", w.Code)
	}
}

// blockingAgents blocks every node run until its context is cancelled.
type blockingAgents struct{ started chan struct{} }

func (b blockingAgents) RunNode(ctx context.Context, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

/* ---------- start validation, access ---------- */

func TestStartValidation(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"missing input":      {`{}`, 400},
		"empty brief":        {`{"input":{"brief":"   "}}`, 400},
		"bad field name":     {`{"input":{"bad key":"x"}}`, 400},
		"unknown pipeline":   {`{"pipeline_id":"nope","input":{"brief":"x"}}`, 404},
		"unknown thread":     {`{"thread_id":"nope","input":{"brief":"x"}}`, 404},
		"bad image":          {`{"input":{"brief":"x","ref":"data:image/png;base64,@@@"}}`, 400},
		"unknown artifact":   {`{"input":{"brief":{"artifact_id":"nope"}}}`, 400},
		"test run for users": {`{"is_test":true,"input":{"brief":"x"}}`, 403},
	} {
		if _, w := h.start(h.user, tc.body); w.Code != tc.code {
			t.Errorf("%s: code = %d (%s), want %d", name, w.Code, w.Body, tc.code)
		}
	}
	if w := h.do("POST", "/runs", `{"input":{"brief":"x"}}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", w.Code)
	}
}

func TestImageInputBecomesBlobReference(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	id, w := h.start(h.user, `{"input":{"brief":"match this","references":"data:image/png;base64,`+png+`"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	h.waitDone(id)
	d := h.detail(id)
	var in map[string]string
	_ = json.Unmarshal(d.Input, &in)
	if !strings.HasPrefix(in["references"], "wcblob://") || strings.Contains(string(d.Input), "base64") {
		t.Fatalf("stored input = %s", d.Input)
	}
	// The generator receives the reference image as a prompt variable.
	gen := h.agents.callsOf("component-generator")[0]
	if gen.Samples["references"] != in["references"] {
		t.Errorf("generator references = %v", gen.Samples["references"])
	}
}

func TestRunsAreOwnerScoped(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	id, _ := h.start(h.user, `{"input":{"brief":"mine"}}`)
	h.waitDone(id)

	if w := h.do("GET", "/runs/"+id, "", h.other); w.Code != http.StatusNotFound {
		t.Errorf("other user get: %d", w.Code)
	}
	if w := h.do("GET", "/runs/"+id+"/events", "", h.other); w.Code != http.StatusNotFound {
		t.Errorf("other user events: %d", w.Code)
	}
	if w := h.do("POST", "/runs/"+id+"/cancel", "", h.other); w.Code != http.StatusNotFound {
		t.Errorf("other user cancel: %d", w.Code)
	}
	var list struct {
		Items []RunListItem `json:"items"`
	}
	w := h.do("GET", "/runs", "", h.other)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 0 {
		t.Errorf("other user list = %d", len(list.Items))
	}
	w = h.do("GET", "/runs", "", h.admin)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 1 {
		t.Errorf("admin list = %d", len(list.Items))
	}

	d := h.detail(id)
	artID := d.Artifacts[0].ID
	if w := h.do("GET", "/artifacts/"+artID+"/content", "", h.other); w.Code != http.StatusNotFound {
		t.Errorf("other user artifact: %d", w.Code)
	}
	w = h.do("GET", "/artifacts/"+artID+"/content", "", h.user)
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("artifact: %d %v", w.Code, w.Header())
	}
}

func TestListAndDetailShapes(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	id, _ := h.start(h.user, `{"input":{"brief":"shape check"}}`)
	h.waitDone(id)

	w := h.do("GET", "/runs?limit=10&filter[thread_id]="+id, "", h.user)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list: %s", w.Body)
	}
	it := list.Items[0]
	for _, k := range []string{"id", "status", "pipeline", "iterations", "best_score", "usage", "cost", "created_at", "thread_id", "input"} {
		if _, ok := it[k]; !ok {
			t.Errorf("list item lacks %q: %v", k, it)
		}
	}
	if it["thread_id"] != id || it["status"] != "completed" {
		t.Errorf("list item = %v", it)
	}
	if p := it["pipeline"].(map[string]any); p["slug"] != "default" || p["version"] != float64(1) {
		t.Errorf("pipeline ref = %v", p)
	}
	if w := h.do("GET", "/runs?filter[thread_id]=other", "", h.user); !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("thread filter: %s", w.Body)
	}

	w = h.do("GET", "/runs/"+id, "", h.user)
	var detail map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &detail)
	for _, k := range []string{"pipeline_version_id", "is_test", "input", "iteration", "best_artifact_id", "usage", "error", "steps", "artifacts"} {
		if _, ok := detail[k]; !ok {
			t.Errorf("detail lacks %q", k)
		}
	}
	if body, _ := detail["pipeline_body"].(map[string]any); body == nil || len(body["nodes"].([]any)) != 8 {
		t.Errorf("pipeline_body = %v", detail["pipeline_body"])
	}
	if detail["error"] != nil || detail["pipeline_version_id"] == "" {
		t.Errorf("detail error/version = %v / %v", detail["error"], detail["pipeline_version_id"])
	}
}

/* ---------- canvas ---------- */

// canvasPaths lists the thread's live canvas paths.
func (h *harness) canvasPaths(thread string) []string {
	h.t.Helper()
	files, err := h.e.Canvas.Files(context.Background(), thread)
	if err != nil {
		h.t.Fatal(err)
	}
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// lastFeedback is the iteration-1 seed the latest generator call received.
func (h *harness) lastFeedback() string {
	gen := h.agents.callsOf("component-generator")
	fb, _ := gen[len(gen)-1].Samples["feedback"].(string)
	return fb
}

func TestCompletedRunCommitsToTheCanvas(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	first, _ := h.start(h.user, `{"input":{"brief":"A login form"}}`)
	h.waitDone(first)
	if got := h.canvasPaths(first); len(got) != 1 || got[0] != "/login-form/login-form.html" {
		t.Fatalf("canvas = %v", got)
	}
	d := h.detail(first)
	if d.CanvasFolder != "/login-form" || len(d.CanvasPaths) != 1 || d.CanvasPaths[0] != "/login-form/login-form.html" {
		t.Errorf("detail canvas = %q %v", d.CanvasFolder, d.CanvasPaths)
	}
	if d.ContextPaths == nil || len(d.ContextPaths) != 0 || d.CanvasNotes == nil {
		t.Errorf("first run context = %#v notes = %#v, want []", d.ContextPaths, d.CanvasNotes)
	}
	files, _ := h.e.Canvas.Files(context.Background(), first)
	if files[0].Score == nil || *files[0].Score != 9 || files[0].RunID != first || files[0].ArtifactID != *d.BestArtifactID {
		t.Errorf("file = %+v", files[0])
	}

	// A follow-up sees the tree and the latest result; its result is a new folder.
	second, w := h.start(h.user, `{"thread_id":"`+first+`","input":{"brief":"make it dark"}}`)
	if second == "" {
		t.Fatalf("follow-up: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(second)
	if run.ThreadID != first {
		t.Errorf("thread = %s, want %s", run.ThreadID, first)
	}
	fb := h.lastFeedback()
	for _, want := range []string{"## Canvas", "/login-form/\n  login-form.html — score 9.0, message 1", "### Latest result",
		"#### /login-form/login-form.html", "design 1", "### Where your result goes", `"/dark"`} {
		if !strings.Contains(fb, want) {
			t.Errorf("seed lacks %q:\n%s", want, fb)
		}
	}
	gen := h.agents.callsOf("component-generator")
	if c, _ := gen[len(gen)-1].Samples["canvas"].(string); c != fb {
		t.Error("the canvas prompt variable differs from the seed")
	}
	if gen[len(gen)-1].ThreadID != first || gen[0].ThreadID != first {
		t.Errorf("thread ids = %q %q", gen[0].ThreadID, gen[len(gen)-1].ThreadID)
	}
	if got := h.canvasPaths(first); len(got) != 2 || got[0] != "/dark/dark.html" {
		t.Errorf("canvas = %v", got)
	}
	// Other users cannot continue the thread.
	if _, w := h.start(h.other, `{"thread_id":"`+first+`","input":{"brief":"x"}}`); w.Code != http.StatusNotFound {
		t.Errorf("foreign thread: %d", w.Code)
	}
}

func TestSelectedComponentsSeedAndPlaceTheResult(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	first, _ := h.start(h.user, `{"input":{"brief":"A login form"}}`)
	h.waitDone(first)

	second, w := h.start(h.user, `{"thread_id":"`+first+`","context_paths":["/login-form/login-form.html","/login-form"],"input":{"brief":"make it dark"}}`)
	if second == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	h.waitDone(second)
	fb := h.lastFeedback()
	for _, want := range []string{"### Selected components", "#### /login-form/login-form.html", "design 1", `new top-level folder "/dark"`} {
		if !strings.Contains(fb, want) {
			t.Errorf("selection seed lacks %q:\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "### Latest result") {
		t.Errorf("a selection replaces the latest result:\n%s", fb)
	}
	d := h.detail(second)
	if len(d.ContextPaths) != 1 || d.ContextPaths[0] != "/login-form/login-form.html" {
		t.Errorf("context paths = %v", d.ContextPaths)
	}
	// A new version keeps the file name, in a new top-level group, linked to its source.
	if len(d.CanvasPaths) != 1 || d.CanvasPaths[0] != "/dark/login-form.html" || d.CanvasFolder != "/dark" {
		t.Fatalf("canvas paths = %v", d.CanvasPaths)
	}
	files, _ := h.e.Canvas.Files(context.Background(), first)
	for _, f := range files {
		if f.Path == "/dark/login-form.html" && f.SourcePath != "/login-form/login-form.html" {
			t.Errorf("source path = %q", f.SourcePath)
		}
	}

	// Validation: unknown path, no thread, too many, folder expanding past the cap.
	for name, body := range map[string]string{
		"unknown":   `{"thread_id":"` + first + `","context_paths":["/nope.html"],"input":{"brief":"x"}}`,
		"bad path":  `{"thread_id":"` + first + `","context_paths":["relative"],"input":{"brief":"x"}}`,
		"no thread": `{"context_paths":["/login-form"],"input":{"brief":"x"}}`,
		"too many":  `{"thread_id":"` + first + `","context_paths":[` + strings.Repeat(`"/login-form",`, 12) + `"/login-form"],"input":{"brief":"x"}}`,
	} {
		if _, w := h.start(h.user, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "context_paths") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, w := h.start(h.other, `{"thread_id":"`+first+`","context_paths":["/login-form"],"input":{"brief":"x"}}`); w.Code != http.StatusNotFound {
		t.Errorf("foreign thread: %d", w.Code)
	}
	var items []canvas.CommitItem
	for range 12 {
		items = append(items, canvas.CommitItem{Name: "b.html", ArtifactID: *d.BestArtifactID})
	}
	if _, _, err := h.e.Canvas.Commit(context.Background(), first, second, canvas.Dest{Folder: "/many"}, items); err != nil {
		t.Fatal(err)
	}
	if _, w := h.start(h.user, `{"thread_id":"`+first+`","context_paths":["/many"],"input":{"brief":"x"}}`); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Errorf("12 files: %d %s", w.Code, w.Body)
	}
	if _, _, err := h.e.Canvas.Commit(context.Background(), first, second, canvas.Dest{Folder: "/many"}, items[:1]); err != nil {
		t.Fatal(err)
	}
	if _, w := h.start(h.user, `{"thread_id":"`+first+`","context_paths":["/many"],"input":{"brief":"x"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("13 files: %d %s", w.Code, w.Body)
	}
}

func TestAgentDestinationHintsWin(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	first, _ := h.start(h.user, `{"input":{"brief":"pricing"}}`)
	h.waitDone(first)

	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		res := htmlResult("<div>tiers</div>")
		res.Folder, res.Names = "Pricing/Dark", []string{"Tiers Card"}
		if strings.Contains(req.Samples["brief"].(string), "three") {
			res.Items = []string{"<a>1</a>", "<a>2</a>", "<a>3</a>"}
			res.Folder, res.Names = "", []string{"one.html", ""}
		}
		return res, nil
	}
	id, _ := h.start(h.user, `{"thread_id":"`+first+`","input":{"brief":"dark"}}`)
	h.waitDone(id)
	if d := h.detail(id); len(d.CanvasPaths) != 1 || d.CanvasPaths[0] != "/pricing-dark/tiers-card.html" || d.CanvasFolder != "/pricing-dark" {
		t.Errorf("hinted = %q %v", d.CanvasFolder, d.CanvasPaths)
	}
	id, _ = h.start(h.user, `{"thread_id":"`+first+`","input":{"brief":"three buttons"}}`)
	h.waitDone(id)
	want := []string{"/three-buttons/one.html", "/three-buttons/three-buttons-2.html", "/three-buttons/three-buttons-3.html"}
	if d := h.detail(id); strings.Join(d.CanvasPaths, ",") != strings.Join(want, ",") {
		t.Errorf("multi = %v, want %v", d.CanvasPaths, want)
	}
}

func TestEveryResultGetsItsOwnTopLevelFolder(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	first, _ := h.start(h.user, `{"input":{"brief":"pricing"}}`)
	h.waitDone(first)

	// Unfinished runs reserve their folder: two parallel changes of /pricing
	// get two new groups, none inside /pricing.
	release := make(chan struct{})
	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		<-release
		return htmlResult("<div>v</div>"), nil
	}
	sel := `{"thread_id":"` + first + `","context_paths":["/pricing/pricing.html"],"input":{"brief":"pricing"}}`
	a, _ := h.start(h.user, sel)
	b, _ := h.start(h.user, sel)
	if fa, fb := h.detail(a).CanvasFolder, h.detail(b).CanvasFolder; fa != "/pricing-2" || fb != "/pricing-3" {
		t.Errorf("reserved folders = %q %q", fa, fb)
	}
	close(release)
	h.waitDone(a)
	h.waitDone(b)
	for id, want := range map[string]string{a: "/pricing-2/pricing.html", b: "/pricing-3/pricing.html"} {
		if d := h.detail(id); len(d.CanvasPaths) != 1 || d.CanvasPaths[0] != want {
			t.Errorf("paths = %v, want %s", d.CanvasPaths, want)
		}
	}

	// A hint naming an existing (nested) folder becomes a new top-level one.
	h.agents.handler = func(slug string, n int, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
		res := htmlResult("<div>h</div>")
		res.Folder = "/pricing"
		return res, nil
	}
	id, _ := h.start(h.user, sel)
	h.waitDone(id)
	if d := h.detail(id); len(d.CanvasPaths) != 1 || d.CanvasPaths[0] != "/pricing-4/pricing.html" {
		t.Errorf("hinted existing folder = %v", d.CanvasPaths)
	}
}

func TestCanvasNotesReachTheNextRunOnce(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	first, _ := h.start(h.user, `{"input":{"brief":"A hero"}}`)
	h.waitDone(first)
	if _, _, err := h.e.Canvas.Delete(context.Background(), first, "user1", []string{"/hero"}); err != nil {
		t.Fatal(err)
	}

	second, _ := h.start(h.user, `{"thread_id":"`+first+`","input":{"brief":"a footer"}}`)
	h.waitDone(second)
	fb := h.lastFeedback()
	for _, want := range []string{"### Changes the user made since the last message", "- deleted /hero/ (1 component: /hero/hero.html)", "(the canvas is empty)"} {
		if !strings.Contains(fb, want) {
			t.Errorf("seed lacks %q:\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "design 1") {
		t.Errorf("the deleted design leaked into the seed:\n%s", fb)
	}
	if d := h.detail(second); len(d.CanvasNotes) != 1 || !strings.HasPrefix(d.CanvasNotes[0], "deleted /hero/") {
		t.Errorf("canvas notes = %v", d.CanvasNotes)
	}

	third, _ := h.start(h.user, `{"thread_id":"`+first+`","input":{"brief":"a header"}}`)
	h.waitDone(third)
	if fb := h.lastFeedback(); strings.Contains(fb, "Changes the user made") {
		t.Errorf("notes delivered twice:\n%s", fb)
	}
	if d := h.detail(third); len(d.CanvasNotes) != 0 {
		t.Errorf("third run notes = %v", d.CanvasNotes)
	}
}

func TestTestRunUsesPipelineDraft(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{9}, []float64{9})
	ctx := context.Background()
	def, _, err := h.defs.LatestPublished(ctx, studio.KindPipeline, "default")
	if err != nil {
		t.Fatal(err)
	}
	// Draft = a minimal pipeline: input -> generator -> output.
	gen, _ := h.defs.GetBySlug(ctx, studio.KindAgent, "component-generator")
	body := studio.PipelineBody{
		Nodes: []studio.PipelineNode{
			{ID: "in", Type: "input", Config: map[string]any{}},
			{ID: "gen", Type: "agent", Config: map[string]any{"agent_id": gen.ID, "label": "Gen"}},
			{ID: "out", Type: "output", Config: map[string]any{}},
		},
		Edges: []studio.PipelineEdge{
			{ID: "a", From: studio.EdgeEndpoint{Node: "in", Port: "brief"}, To: studio.EdgeEndpoint{Node: "gen", Port: "brief"}},
			{ID: "b", From: studio.EdgeEndpoint{Node: "gen", Port: "html"}, To: studio.EdgeEndpoint{Node: "out", Port: "html"}},
		},
	}
	raw, _ := json.Marshal(body)
	if _, err := h.defs.PutDraft(ctx, studio.KindPipeline, def.ID, raw); err != nil {
		t.Fatal(err)
	}

	// Users run the published version; admin test runs run the draft.
	idUser, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	if d := h.detail(idUser); d.Pipeline.Version == nil {
		t.Errorf("user run pipeline version = nil")
	}
	h.waitDone(idUser)

	id, w := h.start(h.admin, `{"is_test":true,"pipeline_id":"`+def.ID+`","input":{"brief":"x"}}`)
	if id == "" {
		t.Fatalf("test run: %d %s", w.Code, w.Body)
	}
	run := h.waitDone(id)
	if run.Status != models.RunCompleted || run.PipelineVersion != nil {
		t.Fatalf("status=%s version=%v err=%s", run.Status, run.PipelineVersion, run.Error)
	}
	d := h.detail(id)
	if len(stepsOf(d, "gen")) != 1 || len(stepsOf(d, "render")) != 0 {
		t.Errorf("steps = %+v", d.Steps)
	}
	if run.BestArtifactID == nil {
		t.Error("draft run has no best artifact")
	}
	// Agents are resolved with draft preference for test runs only.
	gens := h.agents.callsOf("component-generator")
	if gens[len(gens)-1].Draft != true {
		t.Error("test run did not request drafts")
	}
	if gens[0].Draft != false {
		t.Error("user run requested drafts")
	}
}

/* ---------- SSE ---------- */

type sseFrame struct{ event, data string }

func readSSE(t *testing.T, body *bufio.Scanner) []sseFrame {
	t.Helper()
	var frames []sseFrame
	var cur sseFrame
	for body.Scan() {
		line := body.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.event != "" {
				frames = append(frames, cur)
			}
			cur = sseFrame{}
		}
	}
	return frames
}

func TestEventStreamLiveAndReplay(t *testing.T) {
	h := setup(t)
	h.scripted([]float64{5, 9}, []float64{6, 9})
	srv := httptest.NewServer(h.router)
	defer srv.Close()

	id, _ := h.start(h.user, `{"input":{"brief":"x"}}`)
	stream := func() []sseFrame {
		req, _ := http.NewRequest("GET", srv.URL+"/runs/"+id+"/events", nil)
		req.Header.Set("Authorization", h.user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("content-type = %s", ct)
		}
		return readSSE(t, bufio.NewScanner(resp.Body))
	}

	live := stream() // returns when the terminal event closes the stream
	check := func(label string, frames []sseFrame) {
		if len(frames) == 0 {
			t.Fatalf("%s: no frames", label)
		}
		var names []string
		for _, f := range frames {
			names = append(names, f.event)
			var payload map[string]any
			if err := json.Unmarshal([]byte(f.data), &payload); err != nil || payload["event"] != f.event || payload["run_id"] != id {
				t.Fatalf("%s: bad frame %+v (%v)", label, f, err)
			}
		}
		if names[0] != "iteration_started" || names[len(names)-1] != "run_completed" {
			t.Errorf("%s: events = %v", label, names)
		}
		count := func(n string) int {
			c := 0
			for _, x := range names {
				if x == n {
					c++
				}
			}
			return c
		}
		if count("iteration_started") != 2 || count("iteration_completed") != 2 || count("step_started") != count("step_completed") || count("step_started") != 15 {
			t.Errorf("%s: counts: %v", label, names)
		}
		last := frames[len(frames)-1]
		var done map[string]any
		_ = json.Unmarshal([]byte(last.data), &done)
		if paths, _ := done["canvas_paths"].([]any); done["best_artifact_id"] == nil || done["usage"] == nil || len(paths) != 1 || paths[0] != "/x/x.html" {
			t.Errorf("%s: run_completed = %v", label, done)
		}
	}
	check("live", live)

	h.waitDone(id)
	check("replay", stream()) // the bus still has the history

	// Once the bus forgot the run, the terminal state is synthesized.
	h.e.bus.mu.Lock()
	delete(h.e.bus.runs, id)
	h.e.bus.mu.Unlock()
	frames := stream()
	if len(frames) != 1 || frames[0].event != "run_completed" || !strings.Contains(frames[0].data, `"canvas_paths":["/x/x.html"]`) {
		t.Errorf("synthesized = %+v", frames)
	}
}

func TestRecoverInterruptedRuns(t *testing.T) {
	h := setup(t)
	stale := &models.Run{Base: models.Base{ID: models.NewID()}, OwnerID: "user1", Status: models.RunRunning, ThreadID: "t"}
	if err := h.e.DB.Create(stale).Error; err != nil {
		t.Fatal(err)
	}
	step := &models.RunStep{Base: models.Base{ID: models.NewID()}, RunID: stale.ID, Status: models.RunRunning}
	if err := h.e.DB.Create(step).Error; err != nil {
		t.Fatal(err)
	}
	n, err := h.e.RecoverInterrupted(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	var run models.Run
	h.e.DB.First(&run, "id = ?", stale.ID)
	var st models.RunStep
	h.e.DB.First(&st, "id = ?", step.ID)
	if run.Status != models.RunInterrupted || run.Error == "" || st.Status != models.RunInterrupted {
		t.Errorf("run=%s/%q step=%s", run.Status, run.Error, st.Status)
	}
	// A stale non-active run can still be cancelled by its owner.
	stale2 := &models.Run{Base: models.Base{ID: models.NewID()}, OwnerID: "user1", Status: models.RunRunning, ThreadID: "t"}
	h.e.DB.Create(stale2)
	if w := h.do("POST", "/runs/"+stale2.ID+"/cancel", "", h.user); w.Code != http.StatusNoContent {
		t.Errorf("cancel stale: %d", w.Code)
	}
}

/* ---------- units ---------- */

func TestBuildGraphLevelsAndLoop(t *testing.T) {
	h := setup(t)
	def, ver, err := h.defs.LatestPublished(context.Background(), studio.KindPipeline, "default")
	if err != nil {
		t.Fatal(err)
	}
	_ = def
	body, _ := studio.DecodeBody[studio.PipelineBody](ver.Body)
	g, err := buildGraph(body)
	if err != nil {
		t.Fatal(err)
	}
	if g.loop == nil || g.loop.From.Node != "gate" || g.loop.To.Port != "feedback" {
		t.Fatalf("loop = %+v", g.loop)
	}
	want := [][]string{
		{"input"},
		{"generator"},
		{"render", "code-critic"},
		{"vision-critic"},
		{"merge"},
		{"gate"},
		{"output"},
	}
	if fmt.Sprint(g.levels) != fmt.Sprint(want) {
		t.Errorf("levels = %v, want %v", g.levels, want)
	}

	body.Edges = append(body.Edges, studio.PipelineEdge{ID: "cyc", From: studio.EdgeEndpoint{Node: "output", Port: "x"}, To: studio.EdgeEndpoint{Node: "input", Port: "y"}})
	if _, err := buildGraph(body); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle error = %v", err)
	}
}

func TestDecideGate(t *testing.T) {
	cfg := parseGateConfig(map[string]any{"max_iterations": float64(3), "patience": float64(1)})
	if cfg.MinScore != DefaultMinScore {
		t.Fatalf("default min score = %v", cfg.MinScore)
	}
	for name, tc := range map[string]struct {
		in   gateInput
		cont bool
		why  string
	}{
		"reached":     {gateInput{Score: 8, Iteration: 1, HasLoop: true}, false, "reached the target"},
		"below":       {gateInput{Score: 5, Iteration: 1, HasLoop: true}, true, "below the target"},
		"max":         {gateInput{Score: 5, Iteration: 3, HasLoop: true}, false, "maximum of 3"},
		"stagnant":    {gateInput{Score: 5, Iteration: 2, NoImprove: 1, HasLoop: true}, false, "no improvement"},
		"no loop":     {gateInput{Score: 5, Iteration: 1, HasLoop: false}, false, "no loop edge"},
		"over budget": {gateInput{Score: 5, Iteration: 1, HasLoop: true, Cost: 2, CostKnown: true}, false, "budget"},
	} {
		c := cfg
		if name == "over budget" {
			c.BudgetUSD = 1
		}
		cont, why := decideGate(c, tc.in)
		if cont != tc.cont || !strings.Contains(why, tc.why) {
			t.Errorf("%s: cont=%v why=%q", name, cont, why)
		}
	}
	if capped := parseGateConfig(map[string]any{"max_iterations": float64(99)}); capped.MaxIterations != maxIterationsCap {
		t.Errorf("cap = %d", capped.MaxIterations)
	}
}

func TestMergeCritiques(t *testing.T) {
	a := &agentrun.Critique{Strengths: []string{"Clean"}, Score: 4, Issues: []agentrun.CritiqueIssue{
		{Severity: "info", Code: "spacing", Message: "Tight spacing"},
		{Severity: "error", Code: "contrast", Message: "Low contrast"},
	}}
	b := &agentrun.Critique{Strengths: []string{"clean", "Bold"}, Score: 7, Issues: []agentrun.CritiqueIssue{
		{Severity: "warning", Code: "SPACING", Message: "tight spacing", Suggestion: "add 8px"},
	}}
	m := mergeCritiques([]*agentrun.Critique{a, b}, true)
	if m.Score != 5.5 || len(m.Strengths) != 2 || len(m.Issues) != 2 {
		t.Fatalf("merged = %+v", m)
	}
	if m.Issues[0].Code != "contrast" || m.Issues[1].Severity != "warning" || m.Issues[1].Suggestion != "add 8px" {
		t.Errorf("issues = %+v", m.Issues)
	}
	if all := mergeCritiques([]*agentrun.Critique{a, b}, false); len(all.Issues) != 3 {
		t.Errorf("no dedupe issues = %d", len(all.Issues))
	}
	text := formatCritique(m)
	if !strings.Contains(text, "Score: 5.5/10") || !strings.Contains(text, "[error] (contrast) Low contrast") {
		t.Errorf("format:\n%s", text)
	}
}
