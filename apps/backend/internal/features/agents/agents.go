// Package agents wires the agentkit SDK into the app: a "critic" agent backed by the
// Meta Model API, exposed through the SDK's HTTP API (see agentkit/httpapi) at
// /api/v1/agents/critic. Every endpoint requires authentication and sessions are scoped
// to the authenticated user.
package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/fxagent"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/httpapi"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/meta"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

var Module = fx.Options(
	fx.Provide(func(db *gorm.DB) (*sql.DB, error) { return db.DB() }),
	fxagent.SQLiteStore(),
	fxagent.Provide("critic", NewCriticAgent),
	fx.Provide(fx.Annotate(
		NewController,
		fx.ParamTags(`name:"critic"`),
		fx.As(new(httpx.Controller)),
		fx.ResultTags(`group:"controllers"`),
	)),
)

// NewCriticAgent builds the demo agent. The provider is created even without an API key;
// requests then fail at call time, and the controller returns 503 up front.
func NewCriticAgent(cfg *config.Config, s store.Store, log *slog.Logger) (*agentkit.Agent, error) {
	return agentkit.New(agentkit.Config{
		Name:     "critic",
		Provider: meta.New(meta.Config{APIKey: cfg.ModelAPIKey}),
		Model:    cfg.AgentModel,
		System: "You are a writing critic. Analyze the given text and return concise, " +
			"actionable feedback: strengths first, then improvements, then a rewritten example.",
		Store: s,
		Hooks: agentkit.Hooks{
			// Attribute requests to the owning user without leaking their ID.
			BeforeRequest: func(ctx context.Context, sess *store.Session, req *llm.Request) error {
				if owner, ok := sess.Metadata["owner"].(string); ok && owner != "" {
					sum := sha256.Sum256([]byte(owner))
					req.SafetyID = hex.EncodeToString(sum[:8])
				}
				return nil
			},
		},
	})
}

type Controller struct {
	agent *agentkit.Agent
	mw    *auth.Middleware
	cfg   *config.Config
	log   *slog.Logger
}

// NewController consumes the named "critic" agent instance; the annotation in Module reflects that.
func NewController(a *agentkit.Agent, cfg *config.Config, mw *auth.Middleware, log *slog.Logger) *Controller {
	return &Controller{agent: a, mw: mw, cfg: cfg, log: log}
}

func (c *Controller) Routes(r chi.Router) {
	r.Route("/agents/critic", func(r chi.Router) {
		r.Use(c.mw.Required)
		if c.cfg.ModelAPIKey == "" {
			r.Use(unconfigured)
		}
		// httpapi returns a Go 1.22 ServeMux whose patterns are relative to the
		// mount point ("GET /sessions", …). chi's Mount only updates its internal
		// RoutePath — r.URL.Path still holds the full original path when the
		// handler is a plain http.Handler, so the mux would match nothing and
		// answer its own plain-text 404. Rewrite the path to the chi route tail
		// before serving.
		api := httpapi.New(c.agent, httpapi.Options{
			SessionOwner: func(req *http.Request) (string, error) {
				id := auth.UserID(req.Context())
				if id == "" {
					return "", errors.New("agents: no authenticated user")
				}
				return id, nil
			},
			Logger: c.log,
		})
		r.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if rctx := chi.RouteContext(req.Context()); rctx != nil && rctx.RoutePath != "" {
				req.URL.RawPath = ""
				req.URL.Path = rctx.RoutePath
			}
			api.ServeHTTP(w, req)
		}))
	})
}

func unconfigured(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"MODEL_API_KEY is not configured"}}`))
	})
}
