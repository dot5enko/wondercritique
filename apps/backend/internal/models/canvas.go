package models

import (
	"time"

	"gorm.io/gorm"
)

// CanvasFile is one component file of a thread's canvas: a path in the
// thread's file tree pointing at an immutable html artifact. Files are never
// changed in place; a new version of a component is a new row.
//
// It does not embed Base: deletion is a soft delete with an undo group
// (DeleteOp) that queries filter explicitly, not gorm.DeletedAt.
type CanvasFile struct {
	ID         string    `gorm:"primaryKey;type:text"`
	CreatedAt  time.Time `gorm:"index"`
	UpdatedAt  time.Time
	ThreadID   string `gorm:"size:64;uniqueIndex:idx_canvas_live,where:deleted_at IS NULL;index:idx_canvas_thread_path"`
	Path       string `gorm:"size:512;uniqueIndex:idx_canvas_live,where:deleted_at IS NULL;index:idx_canvas_thread_path"` // "/pricing/dark/tiers.html"
	ArtifactID string `gorm:"size:64"`                                                                                    // html artifact holding the content
	RunID      string `gorm:"size:64;index"`                                                                              // run that produced it
	SourcePath string `gorm:"size:512"`                                                                                   // the file it is a new version of ("" = new design)
	Score      *float64
	Size       int64
	DeletedAt  *time.Time `gorm:"index"`
	DeleteOp   string     `gorm:"size:64;index"` // id of the delete operation (restore groups)
}

func (CanvasFile) TableName() string { return "canvas_files" }

// BeforeCreate assigns an ID if none is set.
func (f *CanvasFile) BeforeCreate(*gorm.DB) error {
	if f.ID == "" {
		f.ID = NewID()
	}
	return nil
}

// CanvasState is the per-thread view state of the canvas plus the notes about
// user operations not yet delivered to a run.
type CanvasState struct {
	ThreadID  string       `gorm:"primaryKey;size:64"`
	OwnerID   string       `gorm:"index;size:64"`
	Layout    CanvasLayout `gorm:"serializer:json"`
	Notes     []CanvasNote `gorm:"serializer:json"`
	UpdatedAt time.Time
}

func (CanvasState) TableName() string { return "canvas_states" }

// CanvasLayout is the view-only arrangement of a canvas; agents never see it.
type CanvasLayout struct {
	Pos   map[string]Point   `json:"pos"`   // "g:<folder>" absolute (free layout) | "c:<file>" offset
	Scale map[string]float64 `json:"scale"` // file path → uniform scale
	Dir   map[string]string  `json:"dir"`   // folder path → "row" | "col"
}

// Point is a canvas position or offset.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// CanvasNote tells the next run about a user operation on the canvas.
type CanvasNote struct {
	ID   string    `json:"id"`   // the operation id (delete op / move op)
	Text string    `json:"text"` // "deleted /hero/ (2 components: …)"
	At   time.Time `json:"at"`
}
