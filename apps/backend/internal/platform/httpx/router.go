// Package httpx provides the HTTP router, server lifecycle, middleware and JSON/error helpers.
package httpx

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// APIPrefix is where controllers registered with AsController are mounted.
const APIPrefix = "/api/v1"

// Controller registers routes on a router.
type Controller interface {
	Routes(r chi.Router)
}

// AsController registers a constructor returning a Controller; its routes are mounted under APIPrefix.
func AsController(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.As(new(Controller)), fx.ResultTags(`group:"controllers"`)))
}

// AsRootController registers a constructor returning a Controller mounted at "/" (e.g. probes).
func AsRootController(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.As(new(Controller)), fx.ResultTags(`group:"root_controllers"`)))
}

// Module provides *validator.Validate, *chi.Mux and a lifecycle-managed *http.Server.
var Module = fx.Module("httpx",
	fx.Provide(NewValidator, NewRouter, NewServer),
	fx.Invoke(func(*http.Server) {}),
)

// RouterParams are the dependencies of NewRouter.
type RouterParams struct {
	fx.In
	Config          *config.Config
	Log             *slog.Logger
	Controllers     []Controller `group:"controllers"`
	RootControllers []Controller `group:"root_controllers"`
}

// NewRouter builds the root router: shared middleware, root controllers at "/", the rest under APIPrefix.
func NewRouter(p RouterParams) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, RequestLogger(p.Log), Recoverer(p.Log), Timeout(RequestTimeout))
	if len(p.Config.CORSOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: p.Config.CORSOrigins,
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
			ExposedHeaders: []string{"X-Request-Id", "Content-Disposition"},
			MaxAge:         300,
		}))
	}
	r.NotFound(Handle(func(http.ResponseWriter, *http.Request) error { return apperr.NotFound("route not found") }))
	r.MethodNotAllowed(Handle(func(http.ResponseWriter, *http.Request) error {
		return apperr.New(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}))
	for _, c := range p.RootControllers {
		c.Routes(r)
	}
	r.Route(APIPrefix, func(api chi.Router) {
		for _, c := range p.Controllers {
			c.Routes(api)
		}
	})
	return r
}
