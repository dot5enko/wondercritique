package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

/* ---------- wire shapes (apps/frontend/src/api/runs.ts) ---------- */

// PipelineRef is the pipeline embedded in run responses.
type PipelineRef struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Version *int   `json:"version"`
}

// RunListItem is one row of GET /runs.
type RunListItem struct {
	ID         string             `json:"id"`
	Status     string             `json:"status"`
	Pipeline   PipelineRef        `json:"pipeline"`
	Iterations int                `json:"iterations"`
	BestScore  *float64           `json:"best_score"`
	Usage      *models.TokenUsage `json:"usage"`
	Cost       *float64           `json:"cost"`
	CreatedAt  time.Time          `json:"created_at"`
	ThreadID   string             `json:"thread_id"`
	Input      json.RawMessage    `json:"input"`
	// ContextPaths are the canvas files the message was about.
	ContextPaths []string `json:"context_paths"`
	// CanvasFolder is where the run's results go (went) on the canvas.
	CanvasFolder string `json:"canvas_folder"`
	Mode         string `json:"mode"`   // pipeline | agent
	Reply        string `json:"reply"`  // the chat agent's answer ("" for pipelines)
	Vision       bool   `json:"vision"` // the agent got screenshots of the selection
	// StyleguideID is the chat's style guide ("" = none).
	StyleguideID string `json:"styleguide_id"`
}

func modeOf(r *models.Run) string {
	if r.Mode == "" {
		return models.RunModePipeline
	}
	return r.Mode
}

// StepView is a run step.
type StepView struct {
	ID             string             `json:"id"`
	RunID          string             `json:"run_id"`
	Iteration      int                `json:"iteration"`
	NodeID         string             `json:"node_id"`
	Status         string             `json:"status"`
	PinnedVersions map[string]int     `json:"pinned_versions,omitempty"`
	Model          string             `json:"model,omitempty"`
	AgentSessionID string             `json:"agent_session_id,omitempty"`
	Output         json.RawMessage    `json:"output,omitempty"`
	Usage          *models.TokenUsage `json:"usage"`
	Error          string             `json:"error,omitempty"`
	StartedAt      *time.Time         `json:"started_at,omitempty"`
	CompletedAt    *time.Time         `json:"completed_at,omitempty"`
	ArtifactIDs    []string           `json:"artifact_ids,omitempty"`
}

// ArtifactView is a persisted node output.
type ArtifactView struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Iteration int       `json:"iteration"`
	NodeID    string    `json:"node_id"`
	Kind      string    `json:"kind"`
	BlobSHA   string    `json:"blob_sha"`
	Score     *float64  `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

// RunDetail is GET /runs/{id}.
type RunDetail struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Pipeline          PipelineRef        `json:"pipeline"`
	PipelineVersionID string             `json:"pipeline_version_id"`
	IsTest            bool               `json:"is_test"`
	Input             json.RawMessage    `json:"input"`
	Iteration         int                `json:"iteration"`
	BestArtifactID    *string            `json:"best_artifact_id"`
	Usage             *models.TokenUsage `json:"usage"`
	Cost              *float64           `json:"cost"`
	Error             *string            `json:"error"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	ThreadID          string             `json:"thread_id"`
	PipelineBody      json.RawMessage    `json:"pipeline_body,omitempty"` // the pinned snapshot, for graph labels
	ContextPaths      []string           `json:"context_paths"`
	CanvasFolder      string             `json:"canvas_folder"`
	CanvasNotes       []string           `json:"canvas_notes"`
	CanvasPaths       []string           `json:"canvas_paths"` // files the run committed (live ones)
	Mode              string             `json:"mode"`
	AgentID           string             `json:"agent_id,omitempty"`
	Reply             string             `json:"reply"`
	Vision            bool               `json:"vision"`
	StyleguideID      string             `json:"styleguide_id"`
	Steps             []StepView         `json:"steps"`
	Artifacts         []ArtifactView     `json:"artifacts"`
	// Results are the run's results in display order: one entry (the selected
	// final artifact) or every component of a multi-component result.
	Results []ResultView `json:"results"`
}

// ResultView is one result of a completed run.
type ResultView struct {
	ArtifactID string   `json:"artifact_id"`
	Position   int      `json:"position"`
	Score      *float64 `json:"score"`
}

func pipelineRef(r *models.Run) PipelineRef {
	return PipelineRef{ID: r.PipelineID, Slug: r.PipelineSlug, Name: r.PipelineName, Version: r.PipelineVersion}
}

/* ---------- queries ---------- */

// scoped restricts a runs query to what the viewer may see.
func scoped(q *gorm.DB, v Viewer) *gorm.DB {
	if v.Admin {
		return q
	}
	return q.Where("owner_id = ?", v.UserID)
}

// GetRun loads a run the viewer may see (404 otherwise).
func (e *Engine) GetRun(ctx context.Context, id string, v Viewer) (*models.Run, error) {
	var run models.Run
	err := scoped(e.DB.WithContext(ctx), v).First(&run, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.NotFound("run not found")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return &run, nil
}

// RunDetail loads a run with its steps and artifacts.
func (e *Engine) RunDetail(ctx context.Context, id string, v Viewer) (*RunDetail, error) {
	run, err := e.GetRun(ctx, id, v)
	if err != nil {
		return nil, err
	}
	var steps []models.RunStep
	if err := e.DB.WithContext(ctx).Where("run_id = ?", id).Order("created_at ASC, id ASC").Find(&steps).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	var arts []models.Artifact
	if err := e.DB.WithContext(ctx).Where("run_id = ?", id).Order("created_at ASC, id ASC").Find(&arts).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	var results []models.ResultArtifact
	if err := e.DB.WithContext(ctx).Where("run_id = ?", id).Order("position ASC").Find(&results).Error; err != nil {
		return nil, apperr.Internal(err)
	}

	d := &RunDetail{
		ID: run.ID, Status: run.Status, Pipeline: pipelineRef(run), PipelineVersionID: run.PipelineVersionID,
		IsTest: run.IsTest, Input: run.Input, Iteration: run.Iteration, BestArtifactID: run.BestArtifactID,
		Usage: &run.Usage, Cost: run.Cost, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, ThreadID: run.ThreadID, PipelineBody: run.Snapshot,
		ContextPaths: nonNil(run.ContextPaths), CanvasFolder: run.CanvasFolder, CanvasNotes: nonNil(run.CanvasNotes),
		CanvasPaths: []string{}, Mode: modeOf(run), AgentID: run.AgentID, Reply: run.Reply, Vision: run.Vision, StyleguideID: run.StyleguideID,
		Steps: make([]StepView, 0, len(steps)), Artifacts: make([]ArtifactView, 0, len(arts)),
		Results: make([]ResultView, 0, len(results)),
	}
	if e.Canvas != nil {
		if paths, err := e.Canvas.PathsOfRun(ctx, run.ID); err == nil {
			d.CanvasPaths = paths
		}
	}
	for _, r := range results {
		d.Results = append(d.Results, ResultView{ArtifactID: r.ArtifactID, Position: r.Position, Score: r.Score})
	}
	if run.Error != "" {
		msg := run.Error
		d.Error = &msg
	}
	for _, s := range steps {
		d.Steps = append(d.Steps, StepView{
			ID: s.ID, RunID: s.RunID, Iteration: s.Iteration, NodeID: s.NodeID, Status: s.Status,
			PinnedVersions: s.PinnedVersions, Model: s.Model, AgentSessionID: s.AgentSessionID,
			Output: s.Output, Usage: s.Usage, Error: s.Error, StartedAt: s.StartedAt,
			CompletedAt: s.CompletedAt, ArtifactIDs: s.ArtifactIDs,
		})
	}
	for _, a := range arts {
		d.Artifacts = append(d.Artifacts, ArtifactView{
			ID: a.ID, RunID: a.RunID, Iteration: a.Iteration, NodeID: a.NodeID, Kind: a.Kind,
			BlobSHA: a.BlobSHA, Score: a.Score, CreatedAt: a.CreatedAt,
		})
	}
	return d, nil
}

// ListParams filter and page GET /runs.
type ListParams struct {
	Limit    int
	Offset   int
	ThreadID string
}

// ListRuns lists the viewer's runs, newest first.
func (e *Engine) ListRuns(ctx context.Context, v Viewer, p ListParams) ([]RunListItem, error) {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	p.Limit = min(p.Limit, 200)
	q := scoped(e.DB.WithContext(ctx), v).Model(&models.Run{})
	if p.ThreadID != "" {
		q = q.Where("thread_id = ?", p.ThreadID)
	}
	var runs []models.Run
	if err := q.Order("created_at DESC, id DESC").Limit(p.Limit).Offset(max(p.Offset, 0)).Find(&runs).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]RunListItem, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		out = append(out, RunListItem{
			ID: r.ID, Status: r.Status, Pipeline: pipelineRef(r), Iterations: r.Iteration,
			BestScore: r.BestScore, Usage: &r.Usage, Cost: r.Cost, CreatedAt: r.CreatedAt,
			ThreadID: r.ThreadID, Input: r.Input, ContextPaths: nonNil(r.ContextPaths), CanvasFolder: r.CanvasFolder,
			Mode: modeOf(r), Reply: r.Reply, Vision: r.Vision, StyleguideID: r.StyleguideID,
		})
	}
	return out, nil
}

// nonNil keeps empty id lists serializing as [] instead of null.
func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// artifactFor loads an artifact whose run the viewer may see.
func (e *Engine) artifactFor(ctx context.Context, id string, v Viewer) (*models.Artifact, *models.Run, error) {
	var art models.Artifact
	if err := e.DB.WithContext(ctx).First(&art, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, apperr.NotFound("artifact not found")
		}
		return nil, nil, apperr.Internal(err)
	}
	run, err := e.GetRun(ctx, art.RunID, v)
	if err != nil {
		return nil, nil, apperr.NotFound("artifact not found")
	}
	return &art, run, nil
}

// ArtifactBlob returns the content of an artifact the viewer may see.
func (e *Engine) ArtifactBlob(ctx context.Context, id string, v Viewer) (*blobs.Blob, error) {
	art, _, err := e.artifactFor(ctx, id, v)
	if err != nil {
		return nil, err
	}
	b, err := e.Blobs.Get(ctx, art.BlobSHA)
	if err != nil {
		if errors.Is(err, blobs.ErrNotFound) {
			return nil, apperr.NotFound("artifact content not found")
		}
		return nil, apperr.Internal(err)
	}
	return b, nil
}
