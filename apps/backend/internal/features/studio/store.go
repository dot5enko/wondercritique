package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"gorm.io/gorm"
)

// Store persists studio definitions and their immutable versions.
type Store struct {
	db *gorm.DB
}

// NewStore returns a Store over db.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

/* ---------- typed body helpers ---------- */

// DecodeBody strictly decodes a raw body into its typed representation.
// Unknown fields are rejected so stale editor payloads fail loudly.
func DecodeBody[T any](raw json.RawMessage) (T, error) {
	var body T
	if len(raw) == 0 {
		return body, fmt.Errorf("studio: empty body")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return body, apperr.Invalid("invalid definition body", nil).Wrap(err)
	}
	return body, nil
}

// BodyForKind decodes raw into the typed body struct for kind.
func BodyForKind(kind string, raw json.RawMessage) (any, error) {
	switch kind {
	case KindModelSet:
		return DecodeBody[ModelSetBody](raw)
	case KindAgent:
		return DecodeBody[AgentBody](raw)
	case KindPipeline:
		return DecodeBody[PipelineBody](raw)
	case KindSkill:
		return DecodeBody[SkillBody](raw)
	case KindRule:
		return DecodeBody[RuleBody](raw)
	}
	return nil, apperr.Invalid("unknown kind", nil)
}

/* ---------- identity ---------- */

// DefIdentity is the immutable identity part of a definition.
type DefIdentity struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

/* ---------- reads ---------- */

// List returns every definition of kind, oldest first.
func (s *Store) List(ctx context.Context, kind string) ([]models.StudioDef, error) {
	var defs []models.StudioDef
	if err := dbx.Conn(ctx, s.db).
		Where("kind = ?", kind).
		Order("created_at ASC, id ASC").
		Find(&defs).Error; err != nil {
		return nil, fmt.Errorf("studio: list %s: %w", kind, err)
	}
	return defs, nil
}

// Published returns every definition of kind that has a published version.
func (s *Store) Published(ctx context.Context, kind string) ([]models.StudioDef, error) {
	var defs []models.StudioDef
	if err := dbx.Conn(ctx, s.db).
		Where("kind = ? AND latest_version IS NOT NULL AND archived = ?", kind, false).
		Order("created_at ASC, id ASC").
		Find(&defs).Error; err != nil {
		return nil, fmt.Errorf("studio: published %s: %w", kind, err)
	}
	return defs, nil
}

// Get returns one definition by id (404 when missing or of another kind).
func (s *Store) Get(ctx context.Context, kind, id string) (*models.StudioDef, error) {
	var def models.StudioDef
	if err := dbx.Conn(ctx, s.db).
		Where("kind = ? AND id = ?", kind, id).
		First(&def).Error; err != nil {
		return nil, fmt.Errorf("studio: get %s/%s: %w", kind, id, err)
	}
	return &def, nil
}

// GetBySlug returns one definition by slug (404 when missing or of another kind).
func (s *Store) GetBySlug(ctx context.Context, kind, slug string) (*models.StudioDef, error) {
	var def models.StudioDef
	if err := dbx.Conn(ctx, s.db).
		Where("kind = ? AND slug = ?", kind, slug).
		First(&def).Error; err != nil {
		return nil, fmt.Errorf("studio: get %s/%s: %w", kind, slug, err)
	}
	return &def, nil
}

// Versions lists the published versions of a definition, newest first.
func (s *Store) Versions(ctx context.Context, defID string) ([]models.StudioDefVersion, error) {
	var vs []models.StudioDefVersion
	if err := dbx.Conn(ctx, s.db).
		Where("def_id = ?", defID).
		Order("version DESC").
		Find(&vs).Error; err != nil {
		return nil, fmt.Errorf("studio: versions %s: %w", defID, err)
	}
	return vs, nil
}

// Version returns one published version (404 when missing).
func (s *Store) Version(ctx context.Context, defID string, version int) (*models.StudioDefVersion, error) {
	var v models.StudioDefVersion
	if err := dbx.Conn(ctx, s.db).
		Where("def_id = ? AND version = ?", defID, version).
		First(&v).Error; err != nil {
		return nil, fmt.Errorf("studio: version %s@%d: %w", defID, version, err)
	}
	return &v, nil
}

// LatestPublished returns the def (which must have latest_version set) together
// with its newest published version body. Used by the agent runtime.
func (s *Store) LatestPublished(ctx context.Context, kind, slug string) (*models.StudioDef, *models.StudioDefVersion, error) {
	def, err := s.GetBySlug(ctx, kind, slug)
	if err != nil {
		return nil, nil, err
	}
	if def.LatestVer == nil {
		return nil, nil, apperr.NotFound("no published version")
	}
	v, err := s.Version(ctx, def.ID, *def.LatestVer)
	if err != nil {
		return nil, nil, err
	}
	return def, v, nil
}

/* ---------- writes ---------- */

// Create inserts a new definition with an initial draft body.
func (s *Store) Create(ctx context.Context, kind string, idn DefIdentity, draft json.RawMessage) (*models.StudioDef, error) {
	def := models.StudioDef{
		Kind:        kind,
		Slug:        idn.Slug,
		Name:        idn.Name,
		Description: idn.Description,
	}
	if len(draft) > 0 {
		def.Draft = draft
		now := time.Now()
		def.DraftUpdAt = &now
	}
	if err := dbx.Conn(ctx, s.db).Create(&def).Error; err != nil {
		if isUniqueFailure(err) {
			return nil, apperr.Conflict(fmt.Sprintf("a %s with slug %q already exists", kind, idn.Slug))
		}
		return nil, fmt.Errorf("studio: create %s: %w", kind, err)
	}
	return &def, nil
}

// PutDraft replaces the draft body of a definition and bumps draft_updated_at.
func (s *Store) PutDraft(ctx context.Context, kind, id string, draft json.RawMessage) (*models.StudioDef, error) {
	var def models.StudioDef
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		var err error
		def, err = s.getLocked(ctx, kind, id)
		if err != nil {
			return err
		}
		now := time.Now()
		def.Draft = draft
		def.DraftUpdAt = &now
		return dbx.Conn(ctx, s.db).Model(&models.StudioDef{}).
			Where("id = ?", def.ID).
			Updates(map[string]any{"draft": []byte(draft), "draft_upd_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	return &def, nil
}

// Publish validates nothing itself: it stores body as the next immutable
// version, clears the draft (the published content is now the baseline) and
// bumps latest_version, all in one transaction.
func (s *Store) Publish(ctx context.Context, kind, id, note, publishedBy string, body json.RawMessage) (*models.StudioDef, *models.StudioDefVersion, error) {
	var def models.StudioDef
	var ver models.StudioDefVersion
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		var err error
		def, err = s.getLocked(ctx, kind, id)
		if err != nil {
			return err
		}
		next := 1
		if def.LatestVer != nil {
			next = *def.LatestVer + 1
		}
		ver = models.StudioDefVersion{
			DefID:       def.ID,
			Version:     next,
			Body:        body,
			Note:        note,
			PublishedBy: publishedBy,
			PublishedAt: time.Now(),
		}
		if err := dbx.Conn(ctx, s.db).Create(&ver).Error; err != nil {
			return fmt.Errorf("studio: insert version: %w", err)
		}
		def.LatestVer = &next
		def.Draft = nil
		def.DraftUpdAt = nil
		if err := dbx.Conn(ctx, s.db).Model(&models.StudioDef{}).
			Where("id = ?", def.ID).
			Updates(map[string]any{"latest_version": next, "draft": nil, "draft_upd_at": nil}).Error; err != nil {
			return fmt.Errorf("studio: bump latest_version: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &def, &ver, nil
}

// Restore copies a published version's body back into the draft.
func (s *Store) Restore(ctx context.Context, kind, id string, version int) (*models.StudioDef, *models.StudioDefVersion, error) {
	var def models.StudioDef
	var ver models.StudioDefVersion
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		var err error
		def, err = s.getLocked(ctx, kind, id)
		if err != nil {
			return err
		}
		var verp *models.StudioDefVersion
		verp, err = s.Version(ctx, def.ID, version)
		if err != nil {
			return err
		}
		ver = *verp
		if err != nil {
			return err
		}
		now := time.Now()
		def.Draft = ver.Body
		def.DraftUpdAt = &now
		return dbx.Conn(ctx, s.db).Model(&models.StudioDef{}).
			Where("id = ?", def.ID).
			Updates(map[string]any{"draft": []byte(ver.Body), "draft_upd_at": now}).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return &def, &ver, nil
}

// getLocked loads a definition row FOR UPDATE inside the caller's transaction.
func (s *Store) getLocked(ctx context.Context, kind, id string) (models.StudioDef, error) {
	var def models.StudioDef
	q := dbx.Conn(ctx, s.db).Where("kind = ? AND id = ?", kind, id)
	// SQLite has no SELECT … FOR UPDATE; the write serializes on commit.
	if err := q.First(&def).Error; err != nil {
		return models.StudioDef{}, fmt.Errorf("studio: load %s/%s: %w", kind, id, err)
	}
	return def, nil
}

func isUniqueFailure(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "constraint failed: UNIQUE")
}
