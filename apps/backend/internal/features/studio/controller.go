package studio

import (
	"net/http"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

// Controller serves the studio definition API. Everything is admin-only.
//
// ROUTING NOTE: the definition CRUD uses SINGULAR kind segments
// (/studio/model_set, /studio/agent, …) while the action routes owned by the
// agentrun controller use the plural forms the frontend calls
// (/studio/agents/{id}/preview|try, /studio/pipelines/{id}/validate).
type Controller struct {
	store *Store
	mw    *auth.Middleware
}

// NewController builds the studio API controller.
func NewController(store *Store, mw *auth.Middleware) *Controller {
	return &Controller{store: store, mw: mw}
}

// Routes registers the studio routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	r.Route("/studio", func(r chi.Router) {
		r.Use(c.mw.Required, auth.Role("admin"))

		r.Get("/catalog", c.notImplemented("catalog"))
		r.Get("/export", c.notImplemented("export"))
		r.Post("/import", c.notImplemented("import"))

		r.Route("/{kind}", func(r chi.Router) {
			r.Get("/", c.notImplemented("list"))
			r.Post("/", c.notImplemented("create"))
			r.Get("/{id}", c.notImplemented("get"))
			r.Put("/{id}/draft", c.notImplemented("draft"))
			r.Post("/{id}/publish", c.notImplemented("publish"))
			r.Get("/{id}/versions", c.notImplemented("versions"))
			r.Get("/{id}/versions/{version}", c.notImplemented("version"))
			r.Post("/{id}/restore/{version}", c.notImplemented("restore"))
		})
	})
}

func (c *Controller) notImplemented(what string) http.HandlerFunc {
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return apperr.Unavailable("the studio " + what + " API is not implemented yet")
	})
}
