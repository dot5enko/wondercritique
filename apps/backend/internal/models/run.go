package models

import (
	"encoding/json"
	"time"
)

// Run statuses (the agentkit vocabulary plus pending / interrupted).
const (
	RunPending     = "pending"
	RunRunning     = "running"
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunInterrupted = "interrupted"
)

// Artifact kinds.
const (
	ArtifactHTML  = "html"
	ArtifactImage = "image"
	ArtifactJSON  = "json"
)

// TokenUsage mirrors agentkit's llm.Usage on the wire.
type TokenUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens"`
}

// Add accumulates o into u.
func (u *TokenUsage) Add(o TokenUsage) {
	u.InputTokens += o.InputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.OutputTokens += o.OutputTokens
	u.ReasoningTokens += o.ReasoningTokens
}

// Run is one execution of a pipeline for a brief (plan §5).
type Run struct {
	Base
	OwnerID  string `gorm:"index;size:64"`
	ThreadID string `gorm:"index;size:64"` // chat thread; equals the id of the thread's first run

	PipelineID        string `gorm:"size:64"` // studio def id
	PipelineSlug      string
	PipelineName      string
	PipelineVersion   *int            // nil when the run executes a draft
	PipelineVersionID string          `gorm:"size:64"`   // studio_def_versions.id ("" for drafts)
	Snapshot          json.RawMessage `gorm:"type:text"` // the pipeline body executed
	IsTest            bool
	Input             json.RawMessage `gorm:"type:text"`

	Status         string `gorm:"index;size:16"`
	Iteration      int
	BestArtifactID *string `gorm:"size:64"`
	BestScore      *float64
	Usage          TokenUsage `gorm:"serializer:json"`
	Cost           *float64
	Error          string
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// RunStep is one node execution within an iteration.
type RunStep struct {
	Base
	RunID          string `gorm:"index;size:64"`
	Iteration      int
	NodeID         string
	NodeType       string
	Status         string
	PinnedVersions map[string]int `gorm:"serializer:json"`
	Model          string
	AgentSessionID string
	Output         json.RawMessage `gorm:"type:text"`
	Usage          *TokenUsage     `gorm:"serializer:json"`
	Error          string
	StartedAt      *time.Time
	CompletedAt    *time.Time
	ArtifactIDs    []string `gorm:"serializer:json"`
}

// Artifact is a persisted node output; its content lives in the blob store.
type Artifact struct {
	Base
	RunID     string `gorm:"index;size:64"`
	Iteration int
	NodeID    string
	Kind      string
	BlobSHA   string `gorm:"size:64"`
	Score     *float64
}

func (Run) TableName() string      { return "runs" }
func (RunStep) TableName() string  { return "run_steps" }
func (Artifact) TableName() string { return "artifacts" }
