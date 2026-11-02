// Package llm implements the LLM provider layer: models.dev catalog sync,
// enabled-provider state with encrypted API keys, per-provider rate limits,
// the admin settings API, and provider construction for the agent runtime
// (plan.critique-engine.md §3).
package llm

import (
	"net/http"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

// Module wires the provider registry and the admin settings API.
var Module = fx.Options(
	httpx.AsController(NewController),
)

// Controller serves /settings/providers. Everything is admin-only.
type Controller struct {
	mw *auth.Middleware
}

// NewController builds the settings controller.
func NewController(mw *auth.Middleware) *Controller {
	return &Controller{mw: mw}
}

// Routes registers the settings routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	r.Route("/settings/providers", func(r chi.Router) {
		r.Use(c.mw.Required, auth.Role("admin"))
		r.Get("/", httpx.Handle(c.notImplemented))
		r.Put("/{providerID}", httpx.Handle(c.notImplemented))
		r.Post("/{providerID}/test", httpx.Handle(c.notImplemented))
		r.Post("/catalog/refresh", httpx.Handle(c.notImplemented))
	})
}

func (c *Controller) notImplemented(w http.ResponseWriter, r *http.Request) error {
	return apperr.Unavailable("the provider settings API is not implemented yet")
}
