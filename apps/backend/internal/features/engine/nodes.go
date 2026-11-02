package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// Per-node deadlines.
const (
	agentNodeTimeout  = 15 * time.Minute
	renderNodeTimeout = 90 * time.Second
)

// nodeRun is one node execution within an iteration.
type nodeRun struct {
	st     *runState
	it     *iterState
	node   studio.PipelineNode
	step   *models.RunStep
	inputs map[string][]*portVal

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

func (nr *nodeRun) artifactIDs() []string {
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
	nr.artifacts = append(nr.artifacts, a)
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

	req := agentrun.NodeRequest{
		AgentID:    agentID,
		Version:    int(numCfg(cfg, "agent_version", 0)),
		Draft:      nr.st.run.IsTest,
		ModelSetID: strCfg(cfg, "model_set_id", ""),
		Samples:    nr.samples(),
		Owner:      nr.st.run.OwnerID,
		Title:      fmt.Sprintf("%s · %s", nr.st.run.PipelineName, nr.st.g.label(nr.node.ID)),
	}
	out, err := nr.st.e.Agents.RunNode(ctx, req)

	res := &nodeResult{}
	if out != nil {
		u := models.TokenUsage{
			InputTokens: out.Usage.InputTokens, CachedInputTokens: out.Usage.CachedInputTokens,
			OutputTokens: out.Usage.OutputTokens, ReasoningTokens: out.Usage.ReasoningTokens,
		}
		res.usage = &u
		res.session = out.SessionID
		if out.ProviderID != "" {
			res.model = out.Model()
			res.cost = nr.st.e.priceUsage(out.ProviderID, out.ModelID, u)
		}
		res.pinned = map[string]int{}
		if out.AgentVersion > 0 {
			res.pinned["agent"] = out.AgentVersion
		}
		if out.ModelSetVersion > 0 {
			res.pinned["model_set"] = out.ModelSetVersion
		}
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("the agent did not finish within %s", agentNodeTimeout)
		}
		return res, err
	}

	res.ports = map[string]*portVal{}
	switch out.Contract.Type {
	case "artifact":
		a, err := nr.artifact(ctx, models.ArtifactHTML, []byte(out.HTML), "text/html; charset=utf-8", nil)
		if err != nil {
			return res, err
		}
		pv := &portVal{Text: out.HTML}
		res.ports["html"], res.ports["artifact"] = pv, pv
		nr.it.mu.Lock()
		nr.it.candidate = &candidate{nodeID: nr.node.ID, artifactID: a.ID, html: out.HTML}
		nr.it.mu.Unlock()
		res.output = jsonOut(map[string]any{"artifact_id": a.ID, "bytes": len(out.HTML)})
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
