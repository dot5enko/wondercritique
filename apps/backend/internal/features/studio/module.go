// Package studio implements the studio backend: versioned definitions
// (model_set, agent, pipeline, skill, rule) with drafts + immutable published
// versions, the catalog, pipeline validation, defaults seeding and JSON
// bundle export/import (plan.critique-engine.md §4).
package studio

import (
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"go.uber.org/fx"
)

// Module wires the studio definition store and its admin HTTP API.
var Module = fx.Options(
	dbx.AsModel(&models.StudioDef{}),
	dbx.AsModel(&models.StudioDefVersion{}),
	fx.Provide(NewStore),
	httpx.AsController(NewController),
)
