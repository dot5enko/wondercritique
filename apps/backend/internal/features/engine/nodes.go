package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// Per-node deadlines.
const (
	agentNodeTimeout  = 30 * time.Minute
	renderNodeTimeout = 90 * time.Second
)

// nodeRun is one node execution within an iteration.
type nodeRun struct {
	st     *runState
	it     *iterState
	node   studio.PipelineNode
	step   *models.RunStep
	inputs map[string][]*portVal

	amu       sync.Mutex // artifacts are also recorded from agent tool calls (Sink)
	lastLabel string     // last published activity label (guarded by amu)
	artifacts []*models.Artifact
}

// nodeResult is what a node hands back: wire values for downstream nodes plus
// the step's recorded output.
type nodeResult struct {
	ports   map[string]*portVal
	output  json.RawMessage
	usage   *models.TokenUsage
	cost    *float64
	model   string
	session string
	pinned  map[string]int
}

// activity publishes a live "what is this step doing now" label (deduplicated).
func (nr *nodeRun) activity(label string) {
	nr.amu.Lock()
	same := nr.lastLabel == label
	nr.lastLabel = label
	nr.amu.Unlock()
	if same || label == "" {
		return
	}
	nr.st.emit("step_activity", map[string]any{"iteration": nr.it.n, "node_id": nr.node.ID, "label": label})
}

func (nr *nodeRun) artifactIDs() []string {
	nr.amu.Lock()
	defer nr.amu.Unlock()
	if len(nr.artifacts) == 0 {
		return nil
	}
	ids := make([]string, len(nr.artifacts))
	for i, a := range nr.artifacts {
		ids[i] = a.ID
	}
	return ids
}

// exec dispatches on the node type.
func (nr *nodeRun) exec(ctx context.Context) (*nodeResult, error) {
	switch nr.node.Type {
	case "input":
		return nr.execInput()
	case "agent":
		return nr.execAgent(ctx)
	case "validate":
		return nr.execValidate(ctx)
	case "render":
		return nr.execRender(ctx)
	case "merge":
		return nr.execMerge(ctx)
	case "gate":
		return nr.execGate(ctx)
	case "output":
		return nr.execOutput(ctx)
	case "pick_best":
		return nr.execPickBest(ctx)
	}
	return nil, fmt.Errorf("unsupported node type %q", nr.node.Type)
}

func jsonOut(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// combine merges the values wired into one input port: images concatenate,
// texts are joined.
func combine(vals []*portVal) (text string, images []string) {
	var texts []string
	for _, v := range vals {
		if v.Text != "" {
			texts = append(texts, v.Text)
		}
		images = append(images, v.Images...)
	}
	return strings.Join(texts, "\n\n"), images
}

// artifact persists content as a blob + artifact row of this node.
func (nr *nodeRun) artifact(ctx context.Context, kind string, data []byte, mime string, score *float64) (*models.Artifact, error) {
	bg := context.WithoutCancel(ctx)
	sha, err := nr.st.e.Blobs.Put(bg, data, mime)
	if err != nil {
		return nil, fmt.Errorf("store artifact: %w", err)
	}
	a := &models.Artifact{
		Base:      models.Base{ID: models.NewID()},
		RunID:     nr.st.run.ID,
		Iteration: nr.it.n,
		NodeID:    nr.node.ID,
		Kind:      kind,
		BlobSHA:   sha,
		Score:     score,
	}
	if err := nr.st.e.DB.WithContext(bg).Create(a).Error; err != nil {
		return nil, fmt.Errorf("record artifact: %w", err)
	}
	nr.amu.Lock()
	nr.artifacts = append(nr.artifacts, a)
	nr.amu.Unlock()
	nr.it.mu.Lock()
	nr.it.artifactIDs = append(nr.it.artifactIDs, a.ID)
	nr.it.mu.Unlock()
	return a, nil
}

/* ---------- input ---------- */

func (nr *nodeRun) execInput() (*nodeResult, error) {
	in := nr.st.input
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var refs []string
	for _, k := range keys {
		if s, ok := in[k].(string); ok && strings.HasPrefix(s, "wcblob://") {
			refs = append(refs, s)
		}
	}
	return &nodeResult{
		ports: map[string]*portVal{
			"brief":      {Text: briefOf(in)},
			"references": {Images: refs},
		},
		output: jsonOut(in),
	}, nil
}

/* ---------- agent ---------- */

// samples builds the prompt variables of an agent: the run's input fields,
// overridden by what is wired into the node's ports.
func (nr *nodeRun) samples() map[string]any {
	samples := make(map[string]any, len(nr.st.input)+len(nr.inputs))
	for k, v := range nr.st.input {
		samples[k] = v
	}
	if text := nr.st.canvas.text(); text != "" {
		samples["canvas"] = text
	}
	if h := nr.st.canvas.history; h != "" {
		samples["history"] = h
	}
	for port, vals := range nr.inputs {
		text, images := combine(vals)
		if len(images) > 0 && text == "" {
			samples[port] = images
		} else if text != "" {
			samples[port] = text
		}
	}
	return samples
}

func (nr *nodeRun) execAgent(ctx context.Context) (*nodeResult, error) {
	cfg := nr.node.Config
	agentID := strCfg(cfg, "agent_id", "")
	if agentID == "" {
		return nil, errors.New("no agent is selected")
	}
	ctx, cancel := context.WithTimeout(ctx, agentNodeTimeout)
	defer cancel()
	sink := &stepSink{nr: nr}
	ctx = agentrun.WithSink(ctx, sink)

	samples := nr.samples()
	if nr.st.run.Vision {
		if err := nr.visionSamples(ctx, samples); err != nil {
			return &nodeResult{}, err
		}
	}
	req := agentrun.NodeRequest{
		AgentID:    agentID,
		Version:    int(numCfg(cfg, "agent_version", 0)),
		Draft:      nr.st.run.IsTest,
		ModelSetID: strCfg(cfg, "model_set_id", ""),
		Samples:    samples,
		Owner:      nr.st.run.OwnerID,
		ThreadID:   nr.st.run.ThreadID,
		Title:      fmt.Sprintf("%s · %s", nr.st.run.PipelineName, nr.st.g.label(nr.node.ID)),
	}
	if count := int(numCfg(cfg, "count", 1)); count > 1 {
		return nr.execAgentCandidates(ctx, req, count, sink)
	}
	req.OnEvent = func(ev agentkit.Event) { nr.activity(agentrun.ToolActivity(ev)) }
	out, err := nr.st.e.Agents.RunNode(ctx, req)

	res := &nodeResult{}
	if out != nil {
		res.session = out.SessionID
		if out.ProviderID != "" {
			res.model = out.Model()
		}
		res.pinned = pinnedOf(out)
		sink.SubRun(out)
	}
	res.usage, res.cost = sink.totals()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("the agent did not finish within %s", agentNodeTimeout)
		}
		return res, err
	}

	res.ports = map[string]*portVal{}
	switch out.Contract.Type {
	case "artifact":
		nr.st.setDest(nr.it, destHint{folder: out.Folder, names: out.Names})
		if len(out.Items) > 1 {
			// A multi-component result ("give me 5 buttons"): every item is a
			// result; html consumers (render, critics, gate) are skipped.
			items, err := nr.recordItems(ctx, out.Items, nil)
			if err != nil {
				return res, err
			}
			nr.it.mu.Lock()
			nr.it.items = items
			nr.it.mu.Unlock()
			res.ports["candidates"] = &portVal{Items: items}
			res.output = jsonOut(map[string]any{"items": itemsOut(items), "tools": sink.eventList()})
			return res, nil
		}
		a, err := nr.artifact(ctx, models.ArtifactHTML, []byte(out.HTML), "text/html; charset=utf-8", nil)
		if err != nil {
			return res, err
		}
		pv := &portVal{Text: out.HTML}
		cand := &candidate{nodeID: nr.node.ID, artifactID: a.ID, html: out.HTML}
		res.ports["html"], res.ports["artifact"] = pv, pv
		res.ports["candidates"] = &portVal{Items: []*candidate{cand}}
		nr.it.mu.Lock()
		nr.it.candidate = cand
		nr.it.mu.Unlock()
		res.output = jsonOut(map[string]any{"artifact_id": a.ID, "bytes": len(out.HTML), "tools": sink.eventList()})
	case "critique":
		score := out.Critique.Score
		if _, err := nr.artifact(ctx, models.ArtifactJSON, jsonOut(out.Critique), "application/json", &score); err != nil {
			return res, err
		}
		res.ports["critique"] = &portVal{Text: formatCritique(out.Critique), Critique: out.Critique}
		nr.it.mu.Lock()
		nr.it.agentScores = append(nr.it.agentScores, score)
		nr.it.mu.Unlock()
		res.output = jsonOut(out.Critique)
	case "chat":
		// A chat agent: its text is the reply; every component it made with
		// gen_components is a result (nothing is reduced to one).
		items, dest := sink.componentList()
		nr.st.setDest(nr.it, dest)
		if len(items) > 0 {
			nr.it.mu.Lock()
			nr.it.items = items
			nr.it.mu.Unlock()
			res.ports["candidates"] = &portVal{Items: items}
		}
		nr.st.setReply(out.Text)
		res.ports["output"] = &portVal{Text: out.Text}
		res.output = jsonOut(map[string]any{"reply": out.Text, "items": itemsOut(items), "tools": sink.eventList()})
	case "custom":
		if _, err := nr.artifact(ctx, models.ArtifactJSON, out.Fields, "application/json", nil); err != nil {
			return res, err
		}
		res.ports["output"] = &portVal{Text: string(out.Fields)}
		res.output = out.Fields
	default:
		res.ports["output"] = &portVal{Text: out.Text}
		res.output = jsonOut(out.Text)
	}
	return res, nil
}

// execAgentCandidates runs the agent `count` times in parallel (Agent node
// config count > 1). The candidates go out on the candidates port; without a
// Pick best node downstream they are the iteration's (multi-component) result.
func (nr *nodeRun) execAgentCandidates(ctx context.Context, req agentrun.NodeRequest, count int, sink *stepSink) (*nodeResult, error) {
	cands, err := agentrun.GenerateCandidates(ctx, nr.st.e.Agents.RunNode, agentrun.GenerateRequest{
		Base: req, Count: count, Variations: strListCfg(nr.node.Config, "variations"),
	})
	res := &nodeResult{pinned: map[string]int{}}
	var htmls, variations []string
	var failed []map[string]any
	for _, c := range cands {
		if c.Result != nil {
			sink.SubRun(c.Result)
			if res.session == "" && c.Result.SessionID != "" {
				res.session = c.Result.SessionID
				res.pinned = pinnedOf(c.Result)
				if c.Result.ProviderID != "" {
					res.model = c.Result.Model()
				}
			}
		}
		if c.Err != nil {
			failed = append(failed, map[string]any{"candidate": c.Index, "error": c.Err.Error()})
			continue
		}
		htmls = append(htmls, c.HTML)
		variations = append(variations, c.Variation)
	}
	res.usage, res.cost = sink.totals()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("the candidates did not finish within %s", agentNodeTimeout)
		}
		return res, err
	}
	items, err := nr.recordItems(ctx, htmls, variations)
	if err != nil {
		return res, err
	}
	nr.it.mu.Lock()
	nr.it.items = items
	nr.it.mu.Unlock()
	res.ports = map[string]*portVal{"candidates": {Items: items}}
	res.output = jsonOut(map[string]any{"items": itemsOut(items), "failed": failed})
	return res, nil
}

// recordItems stores each HTML as an artifact of this step.
func (nr *nodeRun) recordItems(ctx context.Context, htmls, variations []string) ([]*candidate, error) {
	items := make([]*candidate, 0, len(htmls))
	for i, h := range htmls {
		a, err := nr.artifact(ctx, models.ArtifactHTML, []byte(h), "text/html; charset=utf-8", nil)
		if err != nil {
			return nil, err
		}
		c := &candidate{nodeID: nr.node.ID, artifactID: a.ID, html: h}
		if i < len(variations) {
			c.variation = variations[i]
		}
		items = append(items, c)
	}
	return items, nil
}

func itemsOut(items []*candidate) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, c := range items {
		out[i] = map[string]any{"artifact_id": c.artifactID, "bytes": len(c.html)}
		if c.variation != "" {
			out[i]["variation"] = c.variation
		}
	}
	return out
}

func pinnedOf(out *agentrun.NodeResult) map[string]int {
	p := map[string]int{}
	if out.AgentVersion > 0 {
		p["agent"] = out.AgentVersion
	}
	if out.ModelSetVersion > 0 {
		p["model_set"] = out.ModelSetVersion
	}
	return p
}

/* ---------- sink: what agent tools produce inside a step ---------- */

// stepSink records tool-made artifacts on the step and sums the usage/cost of
// the step's agent runs (the main one and any nested candidate/judge runs).
type stepSink struct {
	nr *nodeRun

	mu        sync.Mutex
	usage     models.TokenUsage
	any       bool
	cost      float64
	costKnown bool
	events    []map[string]any
	comps     []*candidate // gen_components results, in creation order
	dest      destHint     // their folder / names / sources, aligned with comps
}

var _ agentrun.Sink = (*stepSink)(nil)

func (s *stepSink) Component(ctx context.Context, c agentrun.Component) (string, error) {
	a, err := s.nr.artifact(ctx, models.ArtifactHTML, []byte(c.HTML), "text/html; charset=utf-8", nil)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comps = append(s.comps, &candidate{nodeID: s.nr.node.ID, artifactID: a.ID, html: c.HTML, variation: c.Variation})
	if s.dest.folder == "" {
		s.dest.folder = c.Folder
	}
	s.dest.names = append(s.dest.names, c.Name)
	s.dest.sources = append(s.dest.sources, c.Source)
	return a.ID, nil
}

// componentList is what gen_components produced during the step.
func (s *stepSink) componentList() ([]*candidate, destHint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := destHint{folder: s.dest.folder}
	for i := range s.comps {
		d.names = append(d.names, s.dest.names[i])
		d.sources = append(d.sources, s.dest.sources[i])
	}
	if strings.Join(d.names, "") == "" {
		d.names = nil
	}
	return append([]*candidate(nil), s.comps...), d
}

func (s *stepSink) Artifact(ctx context.Context, kind, mime string, data []byte, score *float64) (string, error) {
	a, err := s.nr.artifact(ctx, kind, data, mime, score)
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

func (s *stepSink) SubRun(res *agentrun.NodeResult) {
	if res == nil {
		return
	}
	u := models.TokenUsage{
		InputTokens: res.Usage.InputTokens, CachedInputTokens: res.Usage.CachedInputTokens,
		OutputTokens: res.Usage.OutputTokens, ReasoningTokens: res.Usage.ReasoningTokens,
	}
	var cost *float64
	if res.ProviderID != "" {
		cost = s.nr.st.e.priceUsage(res.ProviderID, res.ModelID, u)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage.Add(u)
	s.any = true
	if cost != nil {
		s.cost += *cost
		s.costKnown = true
	}
}

func (s *stepSink) Activity(label string) { s.nr.activity(label) }

func (s *stepSink) Event(kind string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, map[string]any{"kind": kind, "data": payload})
}

func (s *stepSink) eventList() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil
	}
	return append([]map[string]any(nil), s.events...)
}

// totals is the step's summed usage and cost (nil when nothing ran / unpriced).
func (s *stepSink) totals() (*models.TokenUsage, *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.any {
		return nil, nil
	}
	u := s.usage
	if !s.costKnown {
		return &u, nil
	}
	c := s.cost
	return &u, &c
}

// priceUsage estimates the USD cost of usage on a model (nil when unpriced).
func (e *Engine) priceUsage(providerID, modelID string, u models.TokenUsage) *float64 {
	if e.Costs == nil {
		return nil
	}
	c := e.Costs.ModelCost(providerID, modelID)
	if c == nil {
		return nil
	}
	cost := c.Estimate(u.InputTokens, u.CachedInputTokens, u.OutputTokens)
	return &cost
}

/* ---------- validate ---------- */

func (nr *nodeRun) execValidate(ctx context.Context) (*nodeResult, error) {
	html, _ := combine(nr.inputs["html"])
	if strings.TrimSpace(html) == "" {
		return nil, errors.New("no HTML to validate")
	}
	rep := component.Validate(html)
	report := map[string]any{
		"ok":         rep.OK(),
		"violations": rep.Violations,
		"fragment":   rep.Fragment,
		"text":       component.Format(rep.Violations),
	}
	raw := jsonOut(report)
	if _, err := nr.artifact(ctx, models.ArtifactJSON, raw, "application/json", nil); err != nil {
		return nil, err
	}
	return &nodeResult{
		ports:  map[string]*portVal{"report": {Text: string(raw)}},
		output: raw,
	}, nil
}

/* ---------- render ---------- */

// renderVariants builds the render variants from the node config.
func renderVariants(cfg map[string]any) []render.Variant {
	width := int(numCfg(cfg, "width", 1280))
	dpr := numCfg(cfg, "dpr", 2)
	scheme := strCfg(cfg, "color_scheme", "light")

	var out []render.Variant
	if list, ok := cfg["variants"].([]any); ok {
		for _, raw := range list {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, render.Variant{
				Width:             int(numCfg(m, "width", float64(width))),
				ColorScheme:       strCfg(m, "color_scheme", scheme),
				DeviceScaleFactor: dpr,
			})
		}
	}
	if len(out) == 0 {
		out = []render.Variant{{Width: width, ColorScheme: scheme, DeviceScaleFactor: dpr}}
	}
	// Variant names must be unique within a request.
	seen := map[string]bool{}
	for i := range out {
		v := out[i].Normalize()
		if len(out) > 1 {
			v.Name = fmt.Sprintf("%s-%d", v.ColorScheme, v.Width)
		}
		for seen[v.Name] {
			v.Name = fmt.Sprintf("%s-%d", v.Name, i+1)
		}
		seen[v.Name] = true
		out[i] = v
	}
	return out
}

func (nr *nodeRun) execRender(ctx context.Context) (*nodeResult, error) {
	html, _ := combine(nr.inputs["html"])
	if strings.TrimSpace(html) == "" {
		return nil, errors.New("no HTML to render")
	}
	if nr.st.e.Renderer == nil {
		return nil, errors.New("screenshots are unavailable on this server")
	}
	nr.activity("Rendering screenshots")
	rctx, cancel := context.WithTimeout(ctx, renderNodeTimeout)
	defer cancel()
	result, err := nr.st.e.Renderer.Render(rctx, render.Request{HTML: html, Variants: renderVariants(nr.node.Config)})
	if err != nil {
		switch {
		case errors.Is(err, render.ErrNoBrowser):
			return nil, errors.New("screenshots are unavailable: no Chrome/Chromium was found on the server (set RENDER_CHROME_PATH)")
		case errors.Is(err, render.ErrInvalidRequest):
			return nil, fmt.Errorf("the HTML cannot be rendered: %w", err)
		}
		return nil, fmt.Errorf("render failed: %w", err)
	}

	var images []string
	shots := make([]map[string]any, 0, len(result.Shots))
	allMetrics := make([]map[string]any, 0, len(result.Shots))
	for _, shot := range result.Shots {
		a, err := nr.artifact(ctx, models.ArtifactImage, shot.PNG, "image/png", nil)
		if err != nil {
			return nil, err
		}
		images = append(images, blobs.URL(a.BlobSHA))

		metrics := map[string]any{}
		if raw, err := json.Marshal(shot.Metrics); err == nil {
			_ = json.Unmarshal(raw, &metrics)
		}
		entry := map[string]any{
			"variant": shot.Variant.Name, "width": shot.Width, "height": shot.Height, "artifact_id": a.ID,
		}
		allMetrics = append(allMetrics, map[string]any{"variant": shot.Variant.Name, "metrics": metrics})
		delete(metrics, "text_runs") // keep the recorded step output small
		entry["metrics"] = metrics
		shots = append(shots, entry)
	}
	metricsJSON := jsonOut(allMetrics)
	return &nodeResult{
		ports: map[string]*portVal{
			"images":  {Images: images},
			"metrics": {Text: string(metricsJSON)},
		},
		output: jsonOut(map[string]any{"canvas_version": result.CanvasVersion, "shots": shots}),
	}, nil
}

/* ---------- merge ---------- */

func (nr *nodeRun) execMerge(ctx context.Context) (*nodeResult, error) {
	var cs []*agentrun.Critique
	for _, v := range nr.inputs["critique"] {
		if v.Critique != nil {
			cs = append(cs, v.Critique)
		}
	}
	if len(cs) == 0 {
		return nil, errors.New("no critiques to merge")
	}
	merged := mergeCritiques(cs, boolCfg(nr.node.Config, "dedupe", true))
	score := merged.Score
	if _, err := nr.artifact(ctx, models.ArtifactJSON, jsonOut(merged), "application/json", &score); err != nil {
		return nil, err
	}
	nr.it.mu.Lock()
	nr.it.mergeScore = &score
	nr.it.mu.Unlock()
	return &nodeResult{
		ports:  map[string]*portVal{"critique": {Text: formatCritique(merged), Critique: merged}},
		output: jsonOut(merged),
	}, nil
}

/* ---------- gate ---------- */

func (nr *nodeRun) execGate(_ context.Context) (*nodeResult, error) {
	var crit *agentrun.Critique
	for _, v := range nr.inputs["critique"] {
		if v.Critique != nil {
			crit = v.Critique
			break
		}
	}
	if crit == nil {
		return nil, errors.New("the gate has no critique to judge")
	}
	st := nr.st
	cfg := parseGateConfig(nr.node.Config)

	st.mu.Lock()
	improved := true
	for _, r := range st.records {
		if r.score != nil && crit.Score <= *r.score {
			improved = false
		}
	}
	if len(st.records) == 0 || improved {
		st.noImprove = 0
	} else {
		st.noImprove++
	}
	noImprove := st.noImprove
	st.mu.Unlock()

	cost, known := st.costSoFar()
	cont, reason := decideGate(cfg, gateInput{
		Score: crit.Score, Iteration: nr.it.n, NoImprove: noImprove,
		Cost: cost, CostKnown: known, HasLoop: st.g.loop != nil,
	})

	nr.it.mu.Lock()
	score := crit.Score
	nr.it.gateScore = &score
	cand := nr.it.candidate
	nr.it.mu.Unlock()

	html := ""
	if cand != nil {
		html = cand.html
	}
	res := &nodeResult{ports: map[string]*portVal{}}
	decision := "done"
	if cont {
		decision = "continue"
		fb := &portVal{Text: feedbackText(nr.it.n, crit, html)}
		res.ports["continue"] = fb
		nr.it.mu.Lock()
		nr.it.feedback = fb
		nr.it.mu.Unlock()
	} else {
		res.ports["done"] = &portVal{Text: html}
	}
	nr.it.mu.Lock()
	nr.it.decision = decision
	nr.it.mu.Unlock()
	res.output = jsonOut(map[string]any{"decision": decision, "reason": reason, "score": crit.Score, "iteration": nr.it.n})
	return res, nil
}

/* ---------- output ---------- */

func (nr *nodeRun) execOutput(ctx context.Context) (*nodeResult, error) {
	st := nr.st
	strategy := strCfg(nr.node.Config, "strategy", "best")
	cur := st.currentRecord(nr.it)
	pick := st.selectFinal(cur, strategy)

	// An HTML wired in with no candidate artifact (custom pipelines) still
	// becomes the run's result.
	if html, _ := combine(nr.inputs["html"]); pick == nil && strings.TrimSpace(html) != "" {
		a, err := nr.artifact(ctx, models.ArtifactHTML, []byte(html), "text/html; charset=utf-8", cur.score)
		if err != nil {
			return nil, err
		}
		cur.artifactID = a.ID
		st.mu.Lock()
		st.final = cur
		st.mu.Unlock()
		pick = cur
	}

	out := map[string]any{"strategy": strategy}
	if pick != nil {
		out["iteration"] = pick.n
		out["artifact_id"] = pick.artifactID
		if pick.score != nil {
			out["score"] = *pick.score
		}
	} else {
		out["note"] = "no HTML artifact was produced"
	}
	return &nodeResult{output: jsonOut(out)}, nil
}

/* ---------- pick best ---------- */

// execPickBest renders every candidate on the candidates port and lets a vision
// judge agent pick the best; the winner continues as the iteration's candidate
// (html port) and the set is dropped.
func (nr *nodeRun) execPickBest(ctx context.Context) (*nodeResult, error) {
	var items []*candidate
	for _, v := range nr.inputs["candidates"] {
		items = append(items, v.Items...)
	}
	if len(items) == 0 {
		return nil, errors.New("no candidates to pick from")
	}
	if len(items) > studio.MaxCandidates {
		items = items[:studio.MaxCandidates]
	}
	choose := func(w *candidate) {
		nr.it.mu.Lock()
		nr.it.candidate = w
		nr.it.items = nil
		nr.it.mu.Unlock()
	}
	if len(items) == 1 {
		choose(items[0])
		return &nodeResult{
			ports:  map[string]*portVal{"html": {Text: items[0].html}},
			output: jsonOut(map[string]any{"winner": 1, "artifact_id": items[0].artifactID, "note": "a single candidate passes through"}),
		}, nil
	}

	cfg := nr.node.Config
	ctx, cancel := context.WithTimeout(ctx, agentNodeTimeout)
	defer cancel()

	judgeID := strCfg(cfg, "judge_agent_id", "")
	if judgeID == "" {
		def, err := nr.st.e.Defs.GetBySlug(ctx, studio.KindAgent, studio.DefaultJudgeSlug)
		if err != nil {
			return nil, fmt.Errorf("no judge agent is selected and the default %q agent does not exist", studio.DefaultJudgeSlug)
		}
		judgeID = def.ID
	}

	// Judges compare several screenshots at once: 1x unless configured.
	rcfg := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		rcfg[k] = v
	}
	if _, ok := rcfg["dpr"]; !ok {
		rcfg["dpr"] = 1.0
	}
	variants := renderVariants(rcfg)
	put := func(ctx context.Context, png []byte) (string, error) {
		a, err := nr.artifact(ctx, models.ArtifactImage, png, "image/png", nil)
		if err != nil {
			return "", err
		}
		return blobs.URL(a.BlobSHA), nil
	}
	entries := make([]agentrun.JudgeCandidate, len(items))
	for i, c := range items {
		entries[i] = agentrun.JudgeCandidate{HTML: c.html, Variation: c.variation}
		nr.activity(fmt.Sprintf("Rendering candidate %d/%d", i+1, len(items)))
		rctx, rcancel := context.WithTimeout(ctx, renderNodeTimeout)
		shots, err := agentrun.RenderShots(rctx, nr.st.e.Renderer, c.html, variants, put)
		rcancel()
		if err != nil {
			entries[i].Note = err.Error()
		}
		entries[i].Images = shots
	}

	brief, _ := combine(nr.inputs["brief"])
	if strings.TrimSpace(brief) == "" {
		brief = briefOf(nr.st.input)
	}
	sink := &stepSink{nr: nr}
	ctx = agentrun.WithSink(ctx, sink)
	v, err := agentrun.Judge(ctx, nr.st.e.Agents.RunNode, agentrun.JudgeRequest{
		Base: agentrun.NodeRequest{
			AgentID: judgeID, Draft: nr.st.run.IsTest, Owner: nr.st.run.OwnerID, ThreadID: nr.st.run.ThreadID,
			Title: fmt.Sprintf("%s · %s", nr.st.run.PipelineName, nr.st.g.label(nr.node.ID)),
		},
		Brief:      brief,
		Criteria:   strCfg(cfg, "criteria", ""),
		Candidates: entries,
	})
	res := &nodeResult{}
	if v != nil && v.Result != nil {
		sink.SubRun(v.Result)
		res.session = v.Result.SessionID
		res.pinned = pinnedOf(v.Result)
		if v.Result.ProviderID != "" {
			res.model = v.Result.Model()
		}
	}
	res.usage, res.cost = sink.totals()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("the judge did not finish within %s", agentNodeTimeout)
		}
		return res, err
	}

	winner := items[v.Winner-1]
	ranking := make([]map[string]any, 0, len(v.Ranking))
	for _, r := range v.Ranking {
		ranking = append(ranking, map[string]any{
			"candidate": r.Candidate, "artifact_id": items[r.Candidate-1].artifactID,
			"score": r.Score, "reason": r.Reason,
		})
	}
	verdict := jsonOut(map[string]any{
		"winner": v.Winner, "artifact_id": winner.artifactID, "ranking": ranking, "summary": v.Summary,
	})
	if _, err := nr.artifact(ctx, models.ArtifactJSON, verdict, "application/json", v.WinnerScore()); err != nil {
		return res, err
	}
	choose(winner)
	res.ports = map[string]*portVal{
		"html":    {Text: winner.html},
		"ranking": {Text: string(verdict)},
	}
	res.output = verdict
	return res, nil
}
