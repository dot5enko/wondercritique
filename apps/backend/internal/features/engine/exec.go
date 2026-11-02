package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// portVal is a value travelling along a wire.
type portVal struct {
	Text     string
	Images   []string // wcblob:// refs
	Critique *agentrun.Critique
}

// iterRecord is what a finished iteration contributes to output selection.
type iterRecord struct {
	n          int
	score      *float64
	artifactID string // the iteration's candidate HTML artifact ("" when none)
}

// runState is the mutable state of one pipeline run.
type runState struct {
	e     *Engine
	run   *models.Run
	g     *graph
	input map[string]any
	seed  *portVal

	mu        sync.Mutex
	usage     models.TokenUsage
	cost      float64
	costKnown bool
	records   []*iterRecord
	noImprove int
	loopValue *portVal
	final     *iterRecord
	lastIter  int
}

func newRunState(e *Engine, run *models.Run, g *graph, input map[string]any, seed *portVal) *runState {
	return &runState{e: e, run: run, g: g, input: input, seed: seed}
}

// iterState is the per-iteration working set.
type iterState struct {
	n int

	mu          sync.Mutex
	values      map[string]map[string]*portVal // node -> port -> value (absent = unfired)
	artifactIDs []string
	agentScores []float64
	mergeScore  *float64
	gateScore   *float64
	candidate   *candidate
	decision    string // "", "continue" or "done"
	feedback    *portVal
}

type candidate struct {
	nodeID     string
	artifactID string
	html       string
}

func newIterState(n int) *iterState {
	return &iterState{n: n, values: map[string]map[string]*portVal{}}
}

func (it *iterState) set(node, port string, v *portVal) {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.values[node] == nil {
		it.values[node] = map[string]*portVal{}
	}
	it.values[node][port] = v
}

func (it *iterState) get(node, port string) *portVal {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.values[node][port]
}

// score is the iteration's quality: the gate's, else the merged critique's,
// else the mean of the critics' (nil when nothing was scored).
func (it *iterState) score() *float64 {
	it.mu.Lock()
	defer it.mu.Unlock()
	switch {
	case it.gateScore != nil:
		return it.gateScore
	case it.mergeScore != nil:
		return it.mergeScore
	case len(it.agentScores) > 0:
		var sum float64
		for _, s := range it.agentScores {
			sum += s
		}
		m := math.Round(sum/float64(len(it.agentScores))*100) / 100
		return &m
	}
	return nil
}

/* ---------- run loop ---------- */

func (st *runState) emit(name string, fields map[string]any) {
	st.e.bus.publish(st.run.ID, name, fields)
}

// execute iterates the graph until the gate says done (or the pipeline has no loop).
func (st *runState) execute(ctx context.Context) error {
	st.loopValue = st.seed
	for n := 1; n <= maxIterationsCap; n++ {
		st.lastIter = n
		st.emit("iteration_started", map[string]any{"iteration": n})
		if err := st.e.DB.WithContext(context.WithoutCancel(ctx)).Model(&models.Run{}).
			Where("id = ?", st.run.ID).Update("iteration", n).Error; err != nil {
			return fmt.Errorf("record iteration: %w", err)
		}

		it := newIterState(n)
		if err := st.runIteration(ctx, it); err != nil {
			return err
		}
		st.closeIteration(ctx, it)
		if it.decision != "continue" || st.g.loop == nil {
			break
		}
		st.loopValue = it.feedback
	}
	st.selectFinal(nil, "best")
	return nil
}

// runIteration executes the graph level by level; nodes of a level run in
// parallel and a failure aborts the siblings.
func (st *runState) runIteration(ctx context.Context, it *iterState) error {
	for _, level := range st.g.levels {
		lctx, cancel := context.WithCancel(ctx)
		errs := make([]error, len(level))
		var wg sync.WaitGroup
		for i, id := range level {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := st.execNode(lctx, it, id); err != nil {
					errs[i] = err
					cancel()
				}
			}()
		}
		wg.Wait()
		cancel()

		// Prefer the root failure over siblings that were merely cancelled by it.
		var first error
		for _, err := range errs {
			if err == nil {
				continue
			}
			if first == nil || (errors.Is(first, context.Canceled) && !errors.Is(err, context.Canceled)) {
				first = err
			}
		}
		if first != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return first
		}
	}
	return nil
}

// gather collects a node's inputs from the iteration's wire values; live is
// false when a connected input port received nothing (its source was skipped
// or an unfired gate output), which skips the node.
func (st *runState) gather(it *iterState, id string) (inputs map[string][]*portVal, live bool) {
	inputs = map[string][]*portVal{}
	total := map[string]int{}
	got := map[string]int{}
	for _, e := range st.g.in[id] {
		total[e.To.Port]++
		if v := it.get(e.From.Node, e.From.Port); v != nil {
			inputs[e.To.Port] = append(inputs[e.To.Port], v)
			got[e.To.Port]++
		}
	}
	for port, n := range total {
		if n > 0 && got[port] == 0 {
			return nil, false
		}
	}
	if l := st.g.loop; l != nil && l.To.Node == id && st.loopValue != nil {
		inputs[l.To.Port] = append(inputs[l.To.Port], st.loopValue)
	}
	return inputs, true
}

// execNode runs one node as a persisted, announced step.
func (st *runState) execNode(ctx context.Context, it *iterState, id string) error {
	node := st.g.nodes[id]
	inputs, live := st.gather(it, id)
	if !live {
		return nil
	}

	now := time.Now().UTC()
	step := &models.RunStep{
		Base:      models.Base{ID: models.NewID()},
		RunID:     st.run.ID,
		Iteration: it.n,
		NodeID:    id,
		NodeType:  node.Type,
		Status:    models.RunRunning,
		StartedAt: &now,
	}
	bg := context.WithoutCancel(ctx)
	if err := st.e.DB.WithContext(bg).Create(step).Error; err != nil {
		return fmt.Errorf("record step: %w", err)
	}
	st.emit("step_started", map[string]any{"iteration": it.n, "node_id": id})

	nr := &nodeRun{st: st, it: it, node: node, step: step, inputs: inputs}
	res, err := nr.exec(ctx)

	done := time.Now().UTC()
	step.CompletedAt = &done
	if res != nil {
		step.Model = res.model
		step.AgentSessionID = res.session
		step.PinnedVersions = res.pinned
		step.Output = res.output
		step.Usage = res.usage
		if res.usage != nil {
			st.addUsage(*res.usage, res.cost)
		}
	}
	step.ArtifactIDs = nr.artifactIDs()
	switch {
	case err == nil:
		step.Status = models.RunCompleted
	case errors.Is(err, context.Canceled):
		step.Status = models.RunCancelled
		step.Error = "Cancelled."
	default:
		step.Status = models.RunFailed
		step.Error = err.Error()
	}
	if uerr := st.e.DB.WithContext(bg).Model(&models.RunStep{}).Where("id = ?", step.ID).
		Select("Status", "Error", "CompletedAt", "Model", "AgentSessionID", "PinnedVersions", "Output", "Usage", "ArtifactIDs").
		Updates(step).Error; uerr != nil {
		st.e.Log.Error("engine: persist step", "run", st.run.ID, "node", id, "err", uerr)
	}

	ev := map[string]any{"iteration": it.n, "node_id": id, "status": step.Status}
	if step.Usage != nil {
		ev["usage"] = step.Usage
	}
	if len(step.ArtifactIDs) > 0 {
		ev["artifact_ids"] = step.ArtifactIDs
	}
	if step.Error != "" {
		ev["error"] = step.Error
	}
	st.emit("step_completed", ev)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("%s: %w", st.g.label(id), err)
	}
	if res != nil {
		for port, v := range res.ports {
			it.set(id, port, v)
		}
	}
	return nil
}

// addUsage accumulates run-level usage and cost. cost is nil when the model
// has no price.
func (st *runState) addUsage(u models.TokenUsage, cost *float64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.usage.Add(u)
	if cost != nil {
		st.cost += *cost
		st.costKnown = true
	}
}

func (st *runState) costSoFar() (float64, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cost, st.costKnown
}

// currentRecord is the in-flight iteration as a selection candidate.
func (st *runState) currentRecord(it *iterState) *iterRecord {
	rec := &iterRecord{n: it.n, score: it.score()}
	it.mu.Lock()
	if it.candidate != nil {
		rec.artifactID = it.candidate.artifactID
	}
	it.mu.Unlock()
	return rec
}

// closeIteration records the iteration's score, attaches it to the candidate
// HTML artifact and announces the iteration.
func (st *runState) closeIteration(ctx context.Context, it *iterState) {
	rec := st.currentRecord(it)
	if rec.score != nil && rec.artifactID != "" {
		if err := st.e.DB.WithContext(context.WithoutCancel(ctx)).Model(&models.Artifact{}).
			Where("id = ?", rec.artifactID).Update("score", *rec.score).Error; err != nil {
			st.e.Log.Warn("engine: attach score", "artifact", rec.artifactID, "err", err)
		}
	}
	st.mu.Lock()
	st.records = append(st.records, rec)
	st.mu.Unlock()

	ev := map[string]any{"iteration": it.n}
	if rec.score != nil {
		ev["best_score"] = *rec.score
	}
	it.mu.Lock()
	if len(it.artifactIDs) > 0 {
		ev["artifact_ids"] = append([]string(nil), it.artifactIDs...)
	}
	it.mu.Unlock()
	st.emit("iteration_completed", ev)
}

// selectFinal picks the output iteration unless the Output node already did.
// current optionally adds the in-flight iteration to the candidates.
func (st *runState) selectFinal(current *iterRecord, strategy string) *iterRecord {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.final != nil && current == nil {
		return st.final
	}
	recs := append([]*iterRecord(nil), st.records...)
	if current != nil {
		recs = append(recs, current)
	}
	var pick *iterRecord
	switch strategy {
	case "last":
		for i := len(recs) - 1; i >= 0; i-- {
			if recs[i].artifactID != "" {
				pick = recs[i]
				break
			}
		}
	default: // best score, ties go to the latest iteration
		sorted := append([]*iterRecord(nil), recs...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].n > sorted[j].n })
		for _, r := range sorted {
			if r.artifactID == "" {
				continue
			}
			if pick == nil || (r.score != nil && (pick.score == nil || *r.score > *pick.score)) {
				pick = r
			}
		}
	}
	st.final = pick
	return pick
}

// fillRun copies the outcome counters onto the run row.
func (st *runState) fillRun(run *models.Run) {
	st.mu.Lock()
	defer st.mu.Unlock()
	run.Usage = st.usage
	if st.costKnown {
		c := st.cost
		run.Cost = &c
	}
	run.Iteration = st.lastIter
	if st.final != nil {
		if st.final.artifactID != "" {
			id := st.final.artifactID
			run.BestArtifactID = &id
		}
		run.BestScore = st.final.score
	} else {
		var best *float64
		for _, r := range st.records {
			if r.score != nil && (best == nil || *r.score > *best) {
				s := *r.score
				best = &s
			}
		}
		run.BestScore = best
	}
}

// feedbackText is what the gate hands the loop target on `continue`: the
// critique plus the HTML it was about, so the generator can revise.
func feedbackText(iteration int, c *agentrun.Critique, html string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Feedback on your previous attempt (iteration %d)\n\n%s\n", iteration, formatCritique(c))
	if html != "" {
		fmt.Fprintf(&b, "\n## Previous HTML\n\n```html\n%s\n```\n", truncate(html, maxContextHTML))
	}
	b.WriteString("\nRevise the design to address this feedback and submit the improved version.")
	return b.String()
}
