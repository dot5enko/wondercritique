package models

import (
	"time"
)

// LLMCatalog is the single-row snapshot of the models.dev api.json catalog.
// The row id is always "catalog" (see LLMCatalogRowID).
type LLMCatalog struct {
	ID        string    `gorm:"primaryKey;type:text" json:"id"`
	FetchedAt time.Time `json:"fetched_at"`
	ETag      string    `json:"etag"`
	Body      string    `json:"body" gorm:"type:text"` // raw api.json
}

// LLMCatalogRowID is the fixed primary key of the single LLMCatalog row.
const LLMCatalogRowID = "catalog"

// LLMProvider is the admin-controlled state of one models.dev provider:
// enablement, the encrypted API key, base-URL template variables and a
// requests-per-minute limit (0 = unlimited).
type LLMProvider struct {
	Base
	ProviderID  string            `json:"provider_id" gorm:"uniqueIndex;size:64"`
	Enabled     bool              `json:"enabled"`
	APIKeyEnc   []byte            `json:"-"` // AES-GCM sealed; never returned by the API
	BaseURLVars map[string]string `json:"base_url_vars" gorm:"serializer:json"`
	RPM         int               `json:"rpm"` // 0 = unlimited
}

func (LLMCatalog) TableName() string  { return "llm_catalogs" }
func (LLMProvider) TableName() string { return "llm_providers" }
