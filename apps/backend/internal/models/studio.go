package models

import (
	"encoding/json"
	"time"
)

// StudioDef is a versioned studio definition (kind: model_set | agent | pipeline | skill | rule).
// The draft is the editable working copy; published versions are immutable (StudioDefVersion).
type StudioDef struct {
	Base
	Kind        string          `json:"kind" gorm:"index:idx_studio_defs_kind_slug,unique;size:32"`
	Slug        string          `json:"slug" gorm:"index:idx_studio_defs_kind_slug,unique;size:100"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Draft       json.RawMessage `json:"draft" gorm:"type:text"` // nil when never edited after publish/seed
	DraftUpdAt  *time.Time      `json:"draft_updated_at"`       // set on every draft write
	LatestVer   *int            `json:"latest_version" gorm:"column:latest_version"`
	Archived    bool            `json:"archived"`
}

// StudioDefVersion is an immutable published version of a StudioDef.
type StudioDefVersion struct {
	Base
	DefID       string          `json:"def_id" gorm:"index:idx_studio_versions_def_ver,unique;size:64"`
	Version     int             `json:"version" gorm:"index:idx_studio_versions_def_ver,unique"`
	Body        json.RawMessage `json:"body" gorm:"type:text"`
	Note        string          `json:"note"`
	PublishedBy string          `json:"published_by"`
	PublishedAt time.Time       `json:"published_at"`
}

func (StudioDef) TableName() string        { return "studio_defs" }
func (StudioDefVersion) TableName() string { return "studio_def_versions" }
