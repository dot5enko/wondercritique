// Multi-candidate core shared by the agent tools (generate_components,
// pick_best, critique_component) and the engine (Agent node `count`, Pick best
// node): generate N candidates in parallel, render them, and let a judge agent
// compare the screenshots. Everything runs through a RunNodeFunc so the engine
// can substitute its own (fake-able) runner.
package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// RunNodeFunc runs one agent to completion (Runtime.RunNode or a fake).
type RunNodeFunc func(ctx context.Context, req NodeRequest) (*NodeResult, error)

// candidateConcurrency bounds the parallel worker sessions of one fan-out.
const candidateConcurrency = 4

// maxJudgeHTML caps the HTML shown to a judge when a candidate has no screenshot.
const maxJudgeHTML = 20000

/* ---------- context: sink + node scope ---------- */

// Sink receives what the multi-candidate tools produce while an agent runs
// inside a pipeline step, so the engine can persist it as step artifacts and
// account the sub-runs' usage. Outside pipelines there is no sink.
type Sink interface {
	// Artifact persists content as an artifact of the current step.
	Artifact(ctx context.Context, kind, mime string, data []byte, score *float64) (id string, err error)
	// SubRun accounts a nested agent run (usage, cost).
	SubRun(res *NodeResult)
	// Event records a structured note for the step output (e.g. "explored").
	Event(kind string, payload any)
	// Activity publishes a short live label of what the step is doing now.
	Activity(label string)
	// Component records a component made by gen_components as a result of
	// the step and returns its artifact id.
	Component(ctx context.Context, c Component) (id string, err error)
}

// Component is one result component of a chat agent (gen_components).
type Component struct {
	HTML      string
	Name      string // file name hint ("" = engine default)
	Folder    string // canvas group hint ("" = the run's folder)
	Variation string
	Source    string // the canvas path it is a new version of ("" = new design)
}

type sinkKey struct{}
type scopeKey struct{}

// WithSink attaches a Sink to ctx (the engine does this per agent step).
func WithSink(ctx context.Context, s Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

// activity reports a live label to the step's sink (no-op without one).
func activity(ctx context.Context, format string, args ...any) {
	if s := sinkFrom(ctx); s != nil {
		s.Activity(fmt.Sprintf(format, args...))
	}
}

// ToolActivity maps an agent run event to a live label ("" = no change).
// The multi-candidate tools label themselves.
func ToolActivity(ev agentkit.Event) string {
	switch ev.Type {
	case agentkit.EventRunStarted, agentkit.EventToolCallFinished:
		return "Thinking…"
	case agentkit.EventToolCallStarted:
		if ev.ToolCall == nil {
			return ""
		}
		switch ev.ToolCall.Name {
		case "write_file":
			return "Writing a file"
		case "edit_file":
			return "Editing a file"
		case "read_file", "glob", "grep":
			return "Reading the workspace"
		case "run_js":
			return "Running JavaScript"
		case "validate_component":
			return "Validating the component"
		case "submit_artifact", "submit_critique", "submit_fields":
			return "Submitting the result"
		}
	}
	return ""
}

func sinkFrom(ctx context.Context) Sink {
	s, _ := ctx.Value(sinkKey{}).(Sink)
	return s
}

// nodeScope is what RunNode knows about the run that tools may need.
type nodeScope struct {
	samples map[string]any
	owner   string
	thread  string // chat thread (canvas mount) passed on to nested runs
	worker  bool   // the agent runs as a generate_components worker
}

func withScope(ctx context.Context, s nodeScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

func scopeFrom(ctx context.Context) (nodeScope, bool) {
	s, ok := ctx.Value(scopeKey{}).(nodeScope)
	return s, ok
}

/* ---------- generate ---------- */

// GenerateRequest asks for Count candidates from one generator agent. Base is
// the per-candidate request template (agent, version, samples, owner, title).
type GenerateRequest struct {
	Base       NodeRequest
	Count      int
	Variations []string // optional per-candidate direction hints (index i → candidate i+1)
}

// Candidate is one generated component (Err set when its run failed).
type Candidate struct {
	Index     int // 1-based
	Variation string
	HTML      string
	Result    *NodeResult
	Err       error
}

// variationText is the per-candidate addendum to the generator's input.
func variationText(i, n int, hint string) string {
	if strings.TrimSpace(hint) != "" {
		return fmt.Sprintf("## Variation (candidate %d of %d)\n\nDirection for this candidate: %s\n\nCommit to this direction; other candidates explore other directions.", i, n, hint)
	}
	return fmt.Sprintf("## Variation (candidate %d of %d)\n\nThis is one of %d independent explorations of the brief. Take a distinct, deliberate design direction rather than the most obvious default.", i, n, n)
}

// GenerateCandidates runs Count generator sessions in parallel (bounded) and
// returns every candidate in index order. Individual failures are reported per
// candidate; the error is non-nil only when ctx ends or nothing succeeded.
func GenerateCandidates(ctx context.Context, run RunNodeFunc, req GenerateRequest) ([]Candidate, error) {
	n := min(max(req.Count, 1), studio.MaxCandidates)
	out := make([]Candidate, n)
	sem := make(chan struct{}, candidateConcurrency)
	var wg sync.WaitGroup
	var done atomic.Int32
	activity(ctx, "Generating %d candidates in parallel", n)
	for i := range n {
		hint := ""
		if i < len(req.Variations) {
			hint = strings.TrimSpace(req.Variations[i])
		}
		out[i] = Candidate{Index: i + 1, Variation: hint}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i].Err = ctx.Err()
				return
			}
			r := req.Base
			r.Worker = true
			r.Samples = make(map[string]any, len(req.Base.Samples)+1)
			for k, v := range req.Base.Samples {
				r.Samples[k] = v
			}
			if hint != "" {
				r.Samples["variation"] = hint
			}
			r.Extra = append([]llm.Block(nil), req.Base.Extra...)
			if n > 1 || hint != "" {
				r.Extra = append(r.Extra, llm.TextBlock(variationText(i+1, n, hint)))
			}
			if r.Title != "" {
				r.Title = fmt.Sprintf("%s · candidate %d/%d", r.Title, i+1, n)
			}
			res, err := run(ctx, r)
			activity(ctx, "Generated %d/%d candidates", done.Add(1), n)
			out[i].Result = res
			if err == nil && res != nil && res.HTML == "" {
				err = errors.New("the generator returned no HTML (it needs the artifact output contract)")
			}
			if err != nil {
				out[i].Err = err
				return
			}
			out[i].HTML = res.HTML
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	ok := 0
	var firstErr error
	for _, c := range out {
		if c.Err == nil {
			ok++
		} else if firstErr == nil {
			firstErr = c.Err
		}
	}
	if ok == 0 {
		return out, fmt.Errorf("every candidate failed: %w", firstErr)
	}
	return out, nil
}

/* ---------- render ---------- */

// PutShot stores one screenshot PNG and returns its image ref (wcblob://…).
type PutShot func(ctx context.Context, png []byte) (string, error)

// ErrNoScreenshots means candidates could not be rendered (no renderer/Chrome);
// judging falls back to the HTML source.
var ErrNoScreenshots = errors.New("screenshots are unavailable on this server")

// RenderShots renders html in the given variants and stores every shot.
func RenderShots(ctx context.Context, r render.Renderer, html string, variants []render.Variant, put PutShot) ([]string, error) {
	if r == nil {
		return nil, ErrNoScreenshots
	}
	res, err := r.Render(ctx, render.Request{HTML: html, Variants: variants})
	if err != nil {
		if errors.Is(err, render.ErrNoBrowser) {
			return nil, fmt.Errorf("%w: no Chrome/Chromium was found (set RENDER_CHROME_PATH)", ErrNoScreenshots)
		}
		return nil, fmt.Errorf("render failed: %w", err)
	}
	refs := make([]string, 0, len(res.Shots))
	for _, s := range res.Shots {
		ref, err := put(ctx, s.PNG)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

/* ---------- judge ---------- */

// JudgeCandidate is one entry shown to the judge.
type JudgeCandidate struct {
	Variation string
	HTML      string
	Images    []string // image refs; empty → the judge sees the HTML source
	Note      string   // e.g. a render error
}

// JudgeRequest asks a judge agent (custom contract with winner/ranking) to
// compare candidates. Base carries the agent, owner, title and draft flag.
type JudgeRequest struct {
	Base       NodeRequest
	Brief      string
	Criteria   string
	Candidates []JudgeCandidate
}

// Ranked is one judged candidate (1-based).
type Ranked struct {
	Candidate int     `json:"candidate"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}

// Verdict is the judge's decision.
type Verdict struct {
	Winner  int         `json:"winner"` // 1-based
	Ranking []Ranked    `json:"ranking"`
	Summary string      `json:"summary"`
	Result  *NodeResult `json:"-"`
}

const defaultCriteria = "Overall visual quality and fit to the brief."

// Judge runs the judge agent over the candidates.
func Judge(ctx context.Context, run RunNodeFunc, req JudgeRequest) (*Verdict, error) {
	if len(req.Candidates) < 2 {
		return nil, errors.New("pick_best needs at least two candidates")
	}
	criteria := strings.TrimSpace(req.Criteria)
	if criteria == "" {
		criteria = defaultCriteria
	}
	r := req.Base
	r.Worker = true
	r.Samples = map[string]any{"brief": req.Brief, "criteria": criteria}
	blocks := []llm.Block{llm.TextBlock(fmt.Sprintf("## Candidates\n\n%d candidates follow, each labelled \"Candidate N\".", len(req.Candidates)))}
	for i, c := range req.Candidates {
		var b strings.Builder
		fmt.Fprintf(&b, "### Candidate %d", i+1)
		if c.Variation != "" {
			fmt.Fprintf(&b, "\n\nIntended direction: %s", c.Variation)
		}
		if c.Note != "" {
			fmt.Fprintf(&b, "\n\nNote: %s", c.Note)
		}
		if len(c.Images) == 0 {
			fmt.Fprintf(&b, "\n\nNo screenshot is available; judge the HTML source:\n\n```html\n%s\n```", truncateStr(c.HTML, maxJudgeHTML))
		}
		blocks = append(blocks, llm.TextBlock(b.String()))
		for _, img := range c.Images {
			blocks = append(blocks, llm.ImageBlock(img))
		}
	}
	r.Extra = append(append([]llm.Block(nil), req.Base.Extra...), blocks...)

	activity(ctx, "Judge comparing %d candidates", len(req.Candidates))
	res, err := run(ctx, r)
	if err != nil {
		return &Verdict{Result: res}, err
	}
	v, err := ParseVerdict(res, len(req.Candidates))
	if v != nil {
		v.Result = res
	}
	return v, err
}

// ParseVerdict reads a judge's answer. A missing/out-of-range winner falls back
// to the top-scored ranking entry.
func ParseVerdict(res *NodeResult, n int) (*Verdict, error) {
	if res == nil {
		return nil, errors.New("the judge returned nothing")
	}
	raw := res.Fields
	if len(raw) == 0 {
		raw = json.RawMessage(jsonObject(res.Text))
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("judge %q must use a custom output contract with winner/ranking/summary fields", res.AgentSlug)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("judge answer is not a verdict: %w", err)
	}
	kept := v.Ranking[:0]
	seen := map[int]bool{}
	for _, r := range v.Ranking {
		if r.Candidate >= 1 && r.Candidate <= n && !seen[r.Candidate] {
			seen[r.Candidate] = true
			r.Score = clampScore(r.Score)
			kept = append(kept, r)
		}
	}
	v.Ranking = kept
	if v.Winner < 1 || v.Winner > n {
		if len(v.Ranking) == 0 {
			return nil, errors.New("the judge named no valid winner")
		}
		best := v.Ranking[0]
		for _, r := range v.Ranking[1:] {
			if r.Score > best.Score {
				best = r
			}
		}
		v.Winner = best.Candidate
	}
	// Best first, the winner on top.
	sort.SliceStable(v.Ranking, func(i, j int) bool {
		if (v.Ranking[i].Candidate == v.Winner) != (v.Ranking[j].Candidate == v.Winner) {
			return v.Ranking[i].Candidate == v.Winner
		}
		return v.Ranking[i].Score > v.Ranking[j].Score
	})
	return &v, nil
}

// WinnerScore is the judge's score for the winner (nil when unranked).
func (v *Verdict) WinnerScore() *float64 {
	for _, r := range v.Ranking {
		if r.Candidate == v.Winner {
			s := r.Score
			return &s
		}
	}
	return nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n<!-- … truncated … -->"
}
