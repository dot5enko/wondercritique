package engine

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	llmsvc "github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Module wires the pipeline engine and the runs HTTP API.
var Module = fx.Options(
	dbx.AsModel(&models.Run{}),
	dbx.AsModel(&models.RunStep{}),
	dbx.AsModel(&models.Artifact{}),
	dbx.AsModel(&models.ResultArtifact{}),
	fx.Provide(newEngine),
	httpx.AsController(NewController),
	fx.Invoke(func(*Engine) {}), // construct eagerly so crash recovery runs at start
)

func newEngine(lc fx.Lifecycle, db *gorm.DB, defs *studio.Store, rt *agentrun.Runtime, renderer render.Renderer,
	bl *blobs.Store, cv *canvas.Store, svc *llmsvc.Service, cfg *config.Config, log *slog.Logger) *Engine {
	e := New(Deps{
		DB: db, Defs: defs, Agents: rt, Renderer: renderer, Blobs: bl, Canvas: cv, LLM: svc, Costs: svc,
		Log: log, Concurrency: cfg.EngineConcurrency,
	})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			n, err := e.RecoverInterrupted(ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				log.Warn("engine: marked unfinished runs interrupted", "count", n)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return e.Close(stopCtx)
		},
	})
	return e
}
