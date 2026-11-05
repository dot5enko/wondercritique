// Pipeline-node execution: runs one studio agent to completion inside a
// pipeline run (the engine's Agent node) and parses the result per the agent's
// output contract. Builds are cached per (agent def, version, model set) and
// share the sqlite agentkit store, so every node run keeps a transcript session.
package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

/* ---------- critique ---------- */

// CritiqueIssue is one problem raised by a critic.
type CritiqueIssue struct {
	Severity   string `json:"severity"` // info | warning | error
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Critique is the structured verdict of a critique-contract agent (the
// submit_critique payload).
type Critique struct {
	Strengths []string        `json:"strengths"`
	Issues    []CritiqueIssue `json:"issues"`
	Score     float64         `json:"score"` // 0-10
}

// MaxScore is the top of the critique score scale.
const MaxScore = 10

// ParseCritique parses a critique from a model answer (a bare JSON object,
// optionally fenced). Severity is normalized, the score is clamped to 0..10 and
// must be present.
func ParseCritique(text string) (*Critique, error) {
	raw := jsonObject(text)
	if raw == "" {
		return nil, fmt.Errorf("no JSON object found")
	}
	var in struct {
		Strengths []string `json:"strengths"`
		Issues    []struct {
			Severity   string `json:"severity"`
			Code       string `json:"code"`
			Message    string `json:"message"`
			Suggestion string `json:"suggestion"`
		} `json:"issues"`
		Score *float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("invalid critique JSON: %w", err)
	}
	if in.Score == nil {
		return nil, fmt.Errorf("critique has no score")
	}
	c := &Critique{Strengths: in.Strengths, Issues: []CritiqueIssue{}, Score: clampScore(*in.Score)}
	if c.Strengths == nil {
		c.Strengths = []string{}
	}
	for _, it := range in.Issues {
		sev := strings.ToLower(strings.TrimSpace(it.Severity))
		switch sev {
		case "info", "warning", "error":
		default:
			sev = "warning"
		}
		c.Issues = append(c.Issues, CritiqueIssue{Severity: sev, Code: it.Code, Message: it.Message, Suggestion: it.Suggestion})
	}
	return c, nil
}

func clampScore(s float64) float64 {
	switch {
	case s < 0:
		return 0
	case s > MaxScore:
		return MaxScore
	}
	return s
}

// jsonObject extracts the outermost {...} of s ("" when none).
func jsonObject(s string) string {
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\s*\n(.*?)```")

// ExtractHTML returns the HTML fragment of a model answer: the submitted
// string, or the first fenced block when the model wrapped it in markdown.
func ExtractHTML(text string) (string, error) {
	s := strings.TrimSpace(text)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	if s == "" {
		return "", fmt.Errorf("the answer is empty")
	}
	if !strings.Contains(s, "<") {
		return "", fmt.Errorf("the answer contains no HTML")
	}
	return s, nil
}

/* ---------- node runs ---------- */

// NodeRequest asks for one agent run inside a pipeline.
type NodeRequest struct {
	AgentID    string
	Version    int    // pinned published version; 0 = latest published
	Draft      bool   // test runs: prefer the agent's draft over published versions
	ModelSetID string // optional override of the agent's model set
	Samples    map[string]any
	Owner      string // user id, attributed to model requests
	Title      string
	// ThreadID is the chat thread of the run; its canvas is mounted read-only
	// at /canvas for the agent ("" = no canvas).
	ThreadID string
	// StyleguideID is the chat's style guide: appended to the system prompt
	// and mounted at /styleguide/STYLEGUIDE.md ("" = none).
	StyleguideID string
	// OnEvent observes the agent's run events (optional).
	OnEvent func(agentkit.Event)
	// Extra blocks are appended to the built input message (variation hints,
	// labelled candidate screenshots for judges).
	Extra []llm.Block
	// Worker builds the agent without the multi-candidate tools
	// (generate_components / pick_best / critique_component), so nested runs
	// cannot fan out again.
	Worker bool
}

// NodeResult is the parsed outcome of a node run.
type NodeResult struct {
	Text     string          // raw final answer
	HTML     string          // artifact contract (the first item of a set)
	Items    []string        // artifact contract: every component when a set (≥2) was submitted
	Critique *Critique       // critique contract
	Fields   json.RawMessage // custom contract
	// Folder and Names are the agent's canvas destination hints
	// (submit_artifact folder/names; empty when not given).
	Folder    string
	Names     []string
	Contract  studio.OutputContract
	Usage     llm.Usage
	SessionID string
	Steps     int

	AgentSlug       string
	AgentVersion    int // 0 = draft
	ProviderID      string
	ModelID         string
	ModelSetVersion int
}

// Model is "provider/model".
func (n *NodeResult) Model() string { return n.ProviderID + "/" + n.ModelID }

// buildInfo describes how a node agent was built.
type buildInfo struct {
	providerID      string
	modelID         string
	modelSetVersion int
}

type nodeBuild struct {
	agent *agentkit.Agent
	info  buildInfo
}

// resolveForNode resolves the agent body for a node request. usedDraft reports
// that the body came from the (unpublished) draft.
func (r *Runtime) resolveForNode(ctx context.Context, req NodeRequest) (res *resolvedBody, usedDraft bool, err error) {
	switch {
	case req.Version > 0:
		def, err := r.store.Get(ctx, studio.KindAgent, req.AgentID)
		if err != nil {
			return nil, false, err
		}
		ver, err := r.store.Version(ctx, def.ID, req.Version)
		if err != nil {
			return nil, false, err
		}
		body, err := studio.DecodeBody[studio.AgentBody](ver.Body)
		if err != nil {
			return nil, false, err
		}
		pinned := *def
		v := ver.Version
		pinned.LatestVer = &v
		return &resolvedBody{Def: &pinned, Body: body}, false, nil
	case req.Draft:
		def, err := r.store.Get(ctx, studio.KindAgent, req.AgentID)
		if err != nil {
			return nil, false, err
		}
		hasDraft := len(def.Draft) > 0 && string(def.Draft) != "null"
		res, err := r.resolveAgent(ctx, req.AgentID, true)
		return res, hasDraft, err
	}
	res, err = r.resolveAgent(ctx, req.AgentID, false)
	return res, false, err
}

// nodeAgent returns the (cached) agent for a node request and a release func
// that drops the read-only material of uncached draft builds.
func (r *Runtime) nodeAgent(ctx context.Context, res *resolvedBody, usedDraft bool, msOverride string, worker bool) (*nodeBuild, func(), error) {
	body := res.Body
	if msOverride != "" {
		body.ModelSetID = msOverride
	}
	if worker {
		body.Tools = withoutCandidateTools(body.Tools)
	}
	build := &resolvedBody{Def: res.Def, Body: body, Draft: usedDraft}
	ver := versionOf(res.Def)

	if usedDraft {
		name := fmt.Sprintf("%s@draft-%s", res.Def.Slug, models.NewID()[:8])
		agent, info, err := r.buildAgentInfo(ctx, build, name, r.agentStore)
		if err != nil {
			return nil, func() {}, err
		}
		return &nodeBuild{agent: agent, info: *info}, func() { r.dropMaterial(name) }, nil
	}

	key := fmt.Sprintf("node|%s|%d|%s|%t", res.Def.ID, ver, body.ModelSetID, worker)
	r.mu.Lock()
	if nb, ok := r.nodeBuilds[key]; ok {
		r.mu.Unlock()
		return nb, func() {}, nil
	}
	r.mu.Unlock()

	name := fmt.Sprintf("%s@v%d", res.Def.Slug, ver)
	if msOverride != "" {
		name += "+" + shortID(msOverride)
	}
	if worker {
		name += "~worker"
	}
	agent, info, err := r.buildAgentInfo(ctx, build, name, r.agentStore)
	if err != nil {
		return nil, func() {}, err
	}
	nb := &nodeBuild{agent: agent, info: *info}
	r.mu.Lock()
	r.nodeBuilds[key] = nb
	r.mu.Unlock()
	return nb, func() {}, nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// dropMaterial forgets the read-only VFS content registered under an agent name.
func (r *Runtime) dropMaterial(name string) {
	r.materialMu.Lock()
	delete(r.material, name)
	r.materialMu.Unlock()
}

// RunNode runs one agent to completion and parses its answer per the output
// contract. A malformed answer gets one corrective follow-up in the same
// session before the node fails.
func (r *Runtime) RunNode(ctx context.Context, req NodeRequest) (*NodeResult, error) {
	res, usedDraft, err := r.resolveForNode(ctx, req)
	if err != nil {
		return nil, err
	}
	nb, release, err := r.nodeAgent(ctx, res, usedDraft, req.ModelSetID, req.Worker)
	defer release()
	if err != nil {
		return nil, err
	}

	sess, err := nb.agent.NewSession(ctx, agentkit.SessionOptions{
		Title:    req.Title,
		Metadata: sessionMetadata(req),
	})
	if err != nil {
		return nil, err
	}

	out := &NodeResult{
		Contract:        res.Body.OutputContract,
		SessionID:       sess.ID,
		AgentSlug:       res.Def.Slug,
		AgentVersion:    versionOf(res.Def),
		ProviderID:      nb.info.providerID,
		ModelID:         nb.info.modelID,
		ModelSetVersion: nb.info.modelSetVersion,
	}
	if usedDraft {
		out.AgentVersion = 0
	}

	first := BuildInput(res.Body.InputTemplate, req.Samples, false)
	first.Blocks = append(first.Blocks, req.Extra...)
	msgs := []llm.Message{first}
	ctx = withScope(ctx, nodeScope{samples: req.Samples, owner: req.Owner, thread: req.ThreadID, styleguide: req.StyleguideID, worker: req.Worker})
	for attempt := 0; ; attempt++ {
		text, usage, steps, err := drive(ctx, nb.agent, sess.ID, msgs, req.OnEvent)
		out.Usage.Add(usage)
		out.Steps += steps
		out.Text = text
		if err != nil {
			return out, err
		}
		perr := out.parse()
		if perr == nil {
			return out, nil
		}
		if attempt >= 1 {
			return out, fmt.Errorf("agent %q: %w", out.AgentSlug, perr)
		}
		msgs = []llm.Message{llm.UserText(nudgeText(out.Contract, perr))}
	}
}

// sessionMetadata is what a node session records: the owner (safety id), the
// thread whose canvas the session mounts and the style guide it follows.
func sessionMetadata(req NodeRequest) map[string]any {
	md := map[string]any{"owner": req.Owner}
	if req.ThreadID != "" {
		md["thread_id"] = req.ThreadID
	}
	if req.StyleguideID != "" {
		md["styleguide_id"] = req.StyleguideID
	}
	return md
}

// drive runs the agent and collects its final answer.
func drive(ctx context.Context, a *agentkit.Agent, sessionID string, msgs []llm.Message, onEvent func(agentkit.Event)) (text string, usage llm.Usage, steps int, err error) {
	for ev, runErr := range a.Run(ctx, sessionID, msgs...) {
		if onEvent != nil {
			onEvent(ev)
		}
		switch ev.Type {
		case agentkit.EventStepFinished:
			steps++
		case agentkit.EventRunFinished:
			text = ev.Text
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case agentkit.EventRunFailed:
			if ev.Usage != nil {
				usage = *ev.Usage
			}
			if ev.Status == store.RunCancelled {
				err = context.Canceled
			}
		}
		if runErr != nil {
			err = runErr
		}
	}
	if ctx.Err() != nil && err == nil {
		err = ctx.Err()
	}
	return text, usage, steps, err
}

// parse fills the contract-specific fields from Text.
func (n *NodeResult) parse() error {
	switch n.Contract.Type {
	case "artifact":
		if set, ok := parseComponentSet(n.Text); ok {
			items := set.Components
			for i, it := range items {
				h, err := ExtractHTML(it)
				if err != nil {
					return fmt.Errorf("component %d: %w", i+1, err)
				}
				items[i] = h
			}
			n.Items = items
			n.HTML = items[0]
			n.Folder, n.Names = set.Folder, set.Names
			return nil
		}
		html, err := ExtractHTML(n.Text)
		if err != nil {
			return err
		}
		n.HTML = html
	case "critique":
		c, err := ParseCritique(n.Text)
		if err != nil {
			return err
		}
		n.Critique = c
	case "custom":
		raw := jsonObject(n.Text)
		if raw == "" {
			return fmt.Errorf("no JSON object found")
		}
		n.Fields = json.RawMessage(raw)
	case "chat":
		// The reply is the text itself; components came through gen_components.
	}
	return nil
}

func nudgeText(contract studio.OutputContract, cause error) string {
	switch contract.Type {
	case "artifact":
		return fmt.Sprintf("Your answer could not be used (%v). Call submit_artifact now with the complete HTML component fragment.", cause)
	case "critique":
		return fmt.Sprintf("Your answer could not be used (%v). Call submit_critique now with strengths, issues and a score from 0 to 10.", cause)
	case "custom":
		return fmt.Sprintf("Your answer could not be used (%v). Call submit_fields now with the required fields.", cause)
	}
	return "Please answer again."
}

// componentSetKey marks a multi-component submission (submit_artifact paths).
const componentSetKey = "components"

// componentSet is the submit_artifact envelope: several components, or any
// number of components with canvas destination hints.
type componentSet struct {
	Components []string `json:"components"`
	Folder     string   `json:"folder,omitempty"`
	Names      []string `json:"names,omitempty"`
}

// parseComponentSet recognises {"components": ["<…>", …]} with ≥2 items, or
// with ≥1 item when a folder or names are given.
func parseComponentSet(text string) (componentSet, bool) {
	var set componentSet
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") {
		return set, false
	}
	var in map[string]json.RawMessage
	if json.Unmarshal([]byte(t), &in) != nil {
		return set, false
	}
	if _, ok := in[componentSetKey]; !ok {
		return set, false
	}
	if json.Unmarshal([]byte(t), &set) != nil {
		return set, false
	}
	hinted := set.Folder != "" || len(set.Names) > 0
	if len(set.Components) == 0 || (len(set.Components) < 2 && !hinted) {
		return set, false
	}
	return set, true
}
