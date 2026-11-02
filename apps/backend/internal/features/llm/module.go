// Package llm implements the LLM provider layer: models.dev catalog sync,
// enabled-provider state with encrypted API keys, per-provider rate limits,
// the admin settings API, and provider construction for the agent runtime
// (plan.critique-engine.md §3).
package llm

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"go.uber.org/fx"
)

// Module wires the provider registry and the admin settings API.
var Module = fx.Options(
	dbx.AsModel(&models.LLMCatalog{}),
	dbx.AsModel(&models.LLMProvider{}),
	fx.Provide(NewService),
	httpx.AsController(NewController),
	fx.Invoke(Startup),
)

// Startup enforces the prod SECRETS_KEY invariant and syncs the models.dev
// catalog when the snapshot is missing or older than 24h. A network failure is
// a warning, never fatal (offline-safe).
func Startup(lc fx.Lifecycle, svc *Service, cfg *config.Config, log *slog.Logger) {
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if !cfg.IsDev() && len(cfg.SecretsKey) < 32 {
			return errors.New("llm: SECRETS_KEY must be set to at least 32 bytes in prod")
		}
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := svc.Sync(syncCtx); err != nil {
			log.Warn("llm: catalog sync failed (continuing offline)", "err", err)
		}
		return nil
	}))
}
