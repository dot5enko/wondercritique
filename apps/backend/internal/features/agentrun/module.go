// Package agentrun builds runnable agentkit agents from published studio
// definitions: prompt assembly (system + rules + skills + output contract),
// provider/model-set resolution, the agent chat mount (/api/v1/agents/{slug})
// and the studio Try-it/preview endpoints (plan.critique-engine.md §4.3, §6).
package agentrun

import (
	"database/sql"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/fxagent"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

// Module wires the agent runtime: the shared agentkit SQLite store, the
// runtime (agent build/cache) and the chat/try controllers.
var Module = fx.Options(
	fx.Provide(func(db *gorm.DB) (*sql.DB, error) { return db.DB() }),
	fxagent.SQLiteStore(),
	fx.Provide(NewRuntime),
	httpx.AsController(NewController),
)
