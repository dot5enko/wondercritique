// Package engine executes pipelines (plan §5): a run pins a pipeline body,
// iterates its node graph (parallel levels, a gate-driven loop edge), persists
// every step and artifact, and streams progress events over SSE.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	llmsvc "github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// NodeRunner runs one studio agent inside a pipeline (implemented by
// *agentrun.Runtime; tests substitute a fake).
type NodeRunner interface {
	RunNode(ctx context.Context, req agentrun.NodeRequest) (*agentrun.NodeResult, error)
}

// Costs prices token usage (implemented by *llm.Service; nil = no cost data).
type Costs interface {
	ModelCost(providerID, modelID string) *llmsvc.ModelCost
}

// Deps are the engine's collaborators.
type Deps struct {
	DB          *gorm.DB
	Defs        *studio.Store
	Agents      NodeRunner
	Renderer    render.Renderer
	Blobs       *blobs.Store
	LLM         *llmsvc.Service // catalog for pipeline validation; may be nil in tests
	Costs       Costs
	Log         *slog.Logger
	Concurrency int
}

// Engine runs pipelines. Safe for concurrent use.
type Engine struct {
	Deps
	bus *eventBus

	root context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
	sem  chan struct{}

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

// New builds the engine. Close stops in-flight runs (they end `interrupted`).
func New(d Deps) *Engine {
	if d.Concurrency <= 0 {
		d.Concurrency = 4
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	root, stop := context.WithCancel(context.Background())
	return &Engine{
		Deps:   d,
		bus:    newEventBus(),
		root:   root,
		stop:   stop,
		sem:    make(chan struct{}, d.Concurrency),
		active: map[string]context.CancelFunc{},
	}
}

// Close cancels running pipelines and waits (bounded) for them to wind down.
func (e *Engine) Close(ctx context.Context) error {
	e.stop()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine: runs did not stop in time: %w", ctx.Err())
	}
}

// RecoverInterrupted marks runs that were pending/running when the process
// died as `interrupted` (plan §5: a crash becomes `interrupted`).
func (e *Engine) RecoverInterrupted(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	res := e.DB.WithContext(ctx).Model(&models.Run{}).
		Where("status IN ?", []string{models.RunPending, models.RunRunning}).
		Updates(map[string]any{
			"status":      models.RunInterrupted,
			"error":       "The server restarted while this run was in progress.",
			"finished_at": now,
		})
	if res.Error != nil {
		return 0, fmt.Errorf("engine: recover runs: %w", res.Error)
	}
	if err := e.DB.WithContext(ctx).Model(&models.RunStep{}).
		Where("status IN ?", []string{models.RunPending, models.RunRunning}).
		Updates(map[string]any{"status": models.RunInterrupted, "completed_at": now}).Error; err != nil {
		return res.RowsAffected, fmt.Errorf("engine: recover steps: %w", err)
	}
	return res.RowsAffected, nil
}

/* ---------- lifecycle ---------- */

// launch starts executing a persisted (pending) run in the background. seed is
// the initial loop value (follow-up context), nil for a fresh thread.
func (e *Engine) launch(run *models.Run, g *graph, input map[string]any, seed *portVal) error {
	if e.root.Err() != nil {
		return apperr.Unavailable("the server is shutting down")
	}
	ctx, cancel := context.WithCancel(e.root)
	e.mu.Lock()
	e.active[run.ID] = cancel
	e.mu.Unlock()
	e.bus.open(run.ID)

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		defer func() {
			e.mu.Lock()
			delete(e.active, run.ID)
			e.mu.Unlock()
		}()
		e.execute(ctx, run, g, input, seed)
	}()
	return nil
}

// execute waits for a worker slot, runs the pipeline and records the outcome.
func (e *Engine) execute(ctx context.Context, run *models.Run, g *graph, input map[string]any, seed *portVal) {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		e.finish(run, nil, ctx.Err())
		return
	}

	now := time.Now().UTC()
	run.Status = models.RunRunning
	run.StartedAt = &now
	if err := e.DB.WithContext(context.WithoutCancel(ctx)).Model(run).
		Updates(map[string]any{"status": run.Status, "started_at": now}).Error; err != nil {
		e.finish(run, nil, fmt.Errorf("record run start: %w", err))
		return
	}

	st := newRunState(e, run, g, input, seed)
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				e.Log.Error("engine: run panicked", "run", run.ID, "panic", r)
				err = fmt.Errorf("internal error while running the pipeline")
			}
		}()
		return st.execute(ctx)
	}()
	e.finish(run, st, err)
}

// finish persists the terminal state and announces it.
func (e *Engine) finish(run *models.Run, st *runState, runErr error) {
	bg := context.Background()
	now := time.Now().UTC()
	run.FinishedAt = &now
	switch {
	case runErr == nil:
		run.Status = models.RunCompleted
	case e.root.Err() != nil:
		run.Status = models.RunInterrupted
		run.Error = "The server stopped while this run was in progress."
	case errors.Is(runErr, context.Canceled):
		run.Status = models.RunCancelled
		run.Error = "Cancelled."
	default:
		run.Status = models.RunFailed
		run.Error = runErr.Error()
	}
	var results []models.ResultArtifact
	if st != nil {
		st.fillRun(run)
		if run.Status == models.RunCompleted {
			results = st.results()
		}
	}
	if len(results) > 0 {
		if err := e.DB.WithContext(bg).Create(&results).Error; err != nil {
			e.Log.Error("engine: persist results", "run", run.ID, "err", err)
		}
	}
	// A struct update with Select applies the usage serializer (map updates would not).
	if err := e.DB.WithContext(bg).Model(&models.Run{}).Where("id = ?", run.ID).
		Select("Status", "Error", "FinishedAt", "Iteration", "BestArtifactID", "BestScore", "Usage", "Cost").
		Updates(run).Error; err != nil {
		e.Log.Error("engine: persist run outcome", "run", run.ID, "err", err)
	}

	if run.Status == models.RunCompleted {
		fields := map[string]any{"usage": run.Usage}
		if run.BestArtifactID != nil {
			fields["best_artifact_id"] = *run.BestArtifactID
		}
		if run.Cost != nil {
			fields["cost"] = *run.Cost
		}
		if len(results) > 0 {
			ids := make([]string, len(results))
			for i, r := range results {
				ids[i] = r.ArtifactID
			}
			fields["result_artifact_ids"] = ids
		}
		e.bus.publish(run.ID, "run_completed", fields)
	} else {
		msg := run.Error
		if msg == "" {
			msg = run.Status
		}
		e.bus.publish(run.ID, "run_failed", map[string]any{"error": msg, "status": run.Status})
	}
	e.bus.closeRun(run.ID)
}

// Cancel stops a running pipeline. Runs that are not executing in this
// process (stale rows) are marked cancelled directly.
func (e *Engine) Cancel(ctx context.Context, id string, viewer Viewer) error {
	run, err := e.GetRun(ctx, id, viewer)
	if err != nil {
		return err
	}
	e.mu.Lock()
	cancel, ok := e.active[run.ID]
	e.mu.Unlock()
	if ok {
		cancel()
		return nil
	}
	if run.Status != models.RunPending && run.Status != models.RunRunning {
		return apperr.Conflict("the run has already finished")
	}
	now := time.Now().UTC()
	return e.DB.WithContext(ctx).Model(&models.Run{}).Where("id = ?", run.ID).
		Updates(map[string]any{"status": models.RunCancelled, "error": "Cancelled.", "finished_at": now}).Error
}
