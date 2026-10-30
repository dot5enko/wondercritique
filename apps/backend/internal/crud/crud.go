// Package crud provides generic REST CRUD resources over GORM models.
//
// Field policy comes from `crud:"create,update,filter,sort"` struct tags; clients use json field names in
// bodies, filter and sort keys. id, created_at, updated_at and deleted_at are always read-only; the exposed
// ones are always filterable and sortable. Untagged fields are server-set only.
package crud

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

// Module provides *crud.Registry.
var Module = fx.Module("crud", fx.Provide(NewRegistry))

// Registry holds the dependencies shared by all CRUD resources.
type Registry struct {
	db       *gorm.DB
	validate *validator.Validate
	log      *slog.Logger
}

func NewRegistry(db *gorm.DB, v *validator.Validate, log *slog.Logger) *Registry {
	return &Registry{db: db, validate: v, log: log}
}

// On binds the registry to a router, typically the one passed to a controller's Routes.
func (g *Registry) On(r chi.Router) *API {
	return &API{Router: r, db: g.db, validate: g.validate, log: g.log}
}

// API is a router plus CRUD dependencies; pass it to NewForType.
type API struct {
	chi.Router
	db       *gorm.DB
	validate *validator.Validate
	log      *slog.Logger
}

// Resource is a mounted CRUD resource.
type Resource[T any] struct {
	Service *Service[T]
	router  chi.Router
}

// Route adds extra endpoints under the resource prefix, behind the resource's Use middleware.
func (r *Resource[T]) Route(fn func(chi.Router)) { r.router.Group(fn) }

// NewForType mounts the CRUD routes for T at prefix:
//
//	GET    /prefix        list
//	GET    /prefix/{id}   get
//	POST   /prefix        create (201)
//	PATCH  /prefix/{id}   partial update
//	DELETE /prefix/{id}   delete (204)
//
// It panics on misconfiguration.
func NewForType[T any](api *API, prefix string, opts ...Option) *Resource[T] {
	st := newSettings(opts)
	svc := newService[T](api.db, api.validate, st)
	h := handlers[T]{svc: svc}
	res := &Resource[T]{Service: svc}
	res.router = api.Route(prefix, func(r chi.Router) {
		r.Use(st.middleware...)
		route := func(op Op, method, pattern string, fn httpx.HandlerFunc) {
			if st.enabled(op) {
				r.With(st.opMiddleware[op]...).Method(method, pattern, httpx.Handle(fn))
			}
		}
		route(OpList, http.MethodGet, "/", h.list)
		route(OpGet, http.MethodGet, "/{id}", h.get)
		route(OpCreate, http.MethodPost, "/", h.create)
		route(OpUpdate, http.MethodPatch, "/{id}", h.update)
		route(OpDelete, http.MethodDelete, "/{id}", h.delete)
	})
	if api.log != nil {
		api.log.Debug("crud resource mounted", "model", svc.meta.name, "prefix", prefix)
	}
	return res
}

type handlers[T any] struct{ svc *Service[T] }

func (h handlers[T]) list(w http.ResponseWriter, r *http.Request) error {
	q, err := ParseListQuery(r.URL.Query())
	if err != nil {
		return err
	}
	page, err := h.svc.List(r.Context(), q)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, page)
}

func (h handlers[T]) get(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, v)
}

func (h handlers[T]) create(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[map[string]json.RawMessage](r)
	if err != nil {
		return err
	}
	v, err := h.svc.Create(r.Context(), body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusCreated, v)
}

func (h handlers[T]) update(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[map[string]json.RawMessage](r)
	if err != nil {
		return err
	}
	v, err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, v)
}

func (h handlers[T]) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusNoContent, nil)
}
