package llm

import (
	"context"
	"net/http"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

/* ---------- wire shapes (apps/frontend/src/api/settings.ts) ---------- */

// ProviderCatalogEntry is one provider as parsed from the models.dev snapshot.
type ProviderCatalogEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	NPM        string `json:"npm,omitempty"`
	Adapter    string `json:"adapter"`
	Env        string `json:"env,omitempty"`
	API        string `json:"api,omitempty"`
	Doc        string `json:"doc,omitempty"`
	ModelCount int    `json:"model_count"`
}

// ProviderState is the enabled/state row for one provider.
type ProviderState struct {
	ProviderID  string            `json:"provider_id"`
	Enabled     bool              `json:"enabled"`
	HasKey      bool              `json:"has_key"`
	BaseURLVars map[string]string `json:"base_url_vars"`
	RPMLimit    *int              `json:"rpm_limit"` // null = unlimited
}

// ProvidersResponse is the GET /settings/providers payload.
type ProvidersResponse struct {
	SyncedAt *string                `json:"synced_at,omitempty"`
	Catalog  []ProviderCatalogEntry `json:"catalog"`
	State    []ProviderState        `json:"state"`
}

// updateProviderBody is the PUT /settings/providers/{id} body.
type updateProviderBody struct {
	Enabled     bool              `json:"enabled"`
	APIKey      *string           `json:"api_key"`
	BaseURLVars map[string]string `json:"base_url_vars"`
	RPMLimit    *int              `json:"rpm_limit"`
}

/* ---------- controller ---------- */

// Controller serves /settings/providers. Everything is admin-only.
type Controller struct {
	mw      *auth.Middleware
	service *Service
}

// NewController builds the settings controller.
func NewController(service *Service, mw *auth.Middleware) *Controller {
	return &Controller{mw: mw, service: service}
}

// Routes registers the settings routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	r.Route("/settings", func(r chi.Router) {
		r.Use(c.mw.Required, auth.Role("admin"))
		r.Get("/setup/status", httpx.Handle(c.setupStatus))
		r.Route("/providers", func(r chi.Router) {
			r.Get("/", httpx.Handle(c.list))
			r.Put("/{providerID}", httpx.Handle(c.update))
			r.Post("/{providerID}/test", httpx.Handle(c.test))
			r.Post("/catalog/refresh", httpx.Handle(c.refresh))
		})
	})
}

// SetupStatus is the first-run wizard probe: {needs_setup} is true when no
// allowed provider is enabled with a key.
func (c *Controller) setupStatus(w http.ResponseWriter, r *http.Request) error {
	needs, enabled, ready := c.service.SetupStatus(r.Context())
	return httpx.JSON(w, http.StatusOK, map[string]any{
		"needs_setup":   needs,
		"enabled_count": enabled,
		"ready_count":   ready,
	})
}

func (c *Controller) list(w http.ResponseWriter, r *http.Request) error {
	out, err := c.service.Snapshot(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (c *Controller) refresh(w http.ResponseWriter, r *http.Request) error {
	if err := c.service.Refresh(r.Context()); err != nil {
		return err
	}
	out, err := c.service.Snapshot(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (c *Controller) update(w http.ResponseWriter, r *http.Request) error {
	providerID := chi.URLParam(r, "providerID")
	body, err := httpx.Decode[updateProviderBody](r)
	if err != nil {
		return err
	}
	state, err := c.service.UpdateProvider(r.Context(), providerID, body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, state)
}

func (c *Controller) test(w http.ResponseWriter, r *http.Request) error {
	providerID := chi.URLParam(r, "providerID")
	// Unknown providers and unsupported adapters answer 404/400 before testing.
	p, ok := c.service.CatalogEntry(providerID)
	if !ok {
		return apperr.NotFound("unknown provider " + providerID)
	}
	if p.Adapter == AdapterNone {
		return apperr.Invalid("provider uses an unsupported adapter", nil)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ok2, detail := c.service.TestProvider(ctx, providerID)
	resp := struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
	}{OK: ok2, Detail: detail}
	return httpx.JSON(w, http.StatusOK, resp)
}
