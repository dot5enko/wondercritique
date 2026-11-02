// Package agentrun builds runnable agentkit agents from published studio
// definitions: prompt assembly (system + rules + skills + output contract),
// provider/model-set resolution, the agent chat mount (/api/v1/agents/{slug})
// and the studio Try-it/preview endpoints (plan.critique-engine.md §4.3, §6).
package agentrun

import (
	"database/sql"
	"net/http"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/fxagent"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

// Module wires the agent runtime: the shared agentkit SQLite store plus the
// chat/try controllers.
var Module = fx.Options(
	fx.Provide(func(db *gorm.DB) (*sql.DB, error) { return db.DB() }),
	fxagent.SQLiteStore(),
	httpx.AsController(NewController),
)

// Controller serves the agent action routes (preview/try/validate) and the
// generic chat mount. Chat endpoints require auth but not admin.
type Controller struct {
	mw *auth.Middleware
}

// NewController builds the agent runtime controller.
func NewController(mw *auth.Middleware) *Controller {
	return &Controller{mw: mw}
}

// Routes registers the runtime routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	// Studio action routes (admin): plural segments by frontend contract.
	r.Route("/studio", func(r chi.Router) {
		r.Use(c.mw.Required, auth.Role("admin"))
		r.Post("/agents/{id}/preview", c.notImplemented("preview"))
		r.Post("/agents/{id}/try", c.notImplemented("try"))
		r.Post("/pipelines/{id}/validate", c.notImplemented("validate"))
	})

	// Chat with any published agent (any authenticated user).
	r.Route("/agents", func(r chi.Router) {
		r.Use(c.mw.Required)
		r.Mount("/{slug}", http.HandlerFunc(c.notImplementedHandler("chat")))
	})
}

func (c *Controller) notImplemented(what string) http.HandlerFunc {
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return apperr.Unavailable("the studio " + what + " API is not implemented yet")
	})
}

func (c *Controller) notImplementedHandler(what string) http.HandlerFunc {
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return apperr.Unavailable("the studio " + what + " API is not implemented yet")
	})
}
