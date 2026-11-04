package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

// Controller serves the studio definition API. Everything is admin-only.
//
// ROUTING NOTE: this controller owns the single `/studio` subrouter (chi
// panics if two controllers both mount `/studio`). Other packages contribute
// routes into it via RouteExtender (see AsRouteExtender). The definition CRUD
// uses SINGULAR kind segments (/studio/model_set, /studio/agent, …) while the
// extender action routes (agentrun) use the plural forms the frontend calls
// (/studio/agents/{id}/preview|try, /studio/pipelines/{id}/validate); chi
// prefers the static `agents`/`pipelines` segments over `{kind}`.
type Controller struct {
	store     *Store
	llm       *llm.Service
	mw        *auth.Middleware
	extenders []RouteExtender
}

// RouteExtender contributes extra routes to the shared `/studio` subrouter.
// Routes registered in StudioRoutes are relative to `/studio` and already run
// behind the admin middleware.
type RouteExtender interface {
	StudioRoutes(r chi.Router)
}

// AsRouteExtender registers a constructor whose result is mounted inside the
// studio subrouter.
func AsRouteExtender(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.As(new(RouteExtender)), fx.ResultTags(`group:"studio_routes"`)))
}

// ControllerParams are the fx dependencies of the studio controller.
type ControllerParams struct {
	fx.In
	Store     *Store
	LLM       *llm.Service
	MW        *auth.Middleware
	Extenders []RouteExtender `group:"studio_routes"`
}

// newControllerFx is the fx constructor (collects route extenders).
func newControllerFx(p ControllerParams) *Controller {
	return NewController(p.Store, p.LLM, p.MW, p.Extenders...)
}

// NewController builds the studio API controller.
func NewController(store *Store, svc *llm.Service, mw *auth.Middleware, extenders ...RouteExtender) *Controller {
	return &Controller{store: store, llm: svc, mw: mw, extenders: extenders}
}

// Routes registers the studio routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	r.Route("/studio", func(r chi.Router) {
		r.Use(c.mw.Required, auth.Role("admin"))

		r.Get("/catalog", httpx.Handle(c.getCatalog))
		r.Get("/export", httpx.Handle(c.export))
		r.Post("/import", httpx.Handle(c.importBundle))
		r.Get("/agents/usage", httpx.Handle(c.agentUsage))

		for _, e := range c.extenders {
			e.StudioRoutes(r)
		}

		r.Route("/{kind}", func(r chi.Router) {
			r.Get("/", httpx.Handle(c.list))
			r.Post("/", httpx.Handle(c.create))
			r.Get("/{id}", httpx.Handle(c.get))
			r.Patch("/{id}", httpx.Handle(c.updateIdentity))
			r.Delete("/{id}", httpx.Handle(c.deleteDef))
			r.Put("/{id}/draft", httpx.Handle(c.putDraft))
			r.Post("/{id}/publish", httpx.Handle(c.publish))
			r.Get("/{id}/versions", httpx.Handle(c.versions))
			r.Get("/{id}/versions/{version}", httpx.Handle(c.version))
			r.Post("/{id}/restore/{version}", httpx.Handle(c.restore))
		})
	})
}

/* ---------- helpers ---------- */

func kindParam(r *http.Request) (string, error) {
	kind := chi.URLParam(r, "kind")
	if !ValidKind(kind) {
		return "", apperr.NotFound("unknown kind " + kind)
	}
	return kind, nil
}

// decodeBodyFor decodes a raw draft/body for kind and validates it the way a
// draft save must (pipelines: structural decode only). It additionally checks
// agent cross-references against the store.
func (c *Controller) decodeBodyFor(ctx context.Context, kind string, raw json.RawMessage, validate bool) (any, error) {
	body, err := BodyForKind(kind, raw)
	if err != nil {
		return nil, err
	}
	if validate {
		if err := validateForDraft(kind, body); err != nil {
			return nil, err
		}
	}
	if kind == KindAgent {
		if err := c.checkAgentRefs(ctx, body.(AgentBody)); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// checkAgentRefs validates an agent body's references against the store:
// model_set_id must exist (any state); skill_ids and rule_ids must reference
// existing defs of that kind.
func (c *Controller) checkAgentRefs(ctx context.Context, b AgentBody) error {
	if _, err := c.store.Get(ctx, KindModelSet, b.ModelSetID); err != nil {
		return invalidf("model_set_id %q does not exist", b.ModelSetID)
	}
	for _, id := range b.SkillIDs {
		if _, err := c.store.Get(ctx, KindSkill, id); err != nil {
			return invalidf("skill %q does not exist", id)
		}
	}
	for _, id := range b.RuleIDs {
		if _, err := c.store.Get(ctx, KindRule, id); err != nil {
			return invalidf("rule %q does not exist", id)
		}
	}
	for field, id := range map[string]string{
		"generate_components.agent_id":       b.GenerateConfig().AgentID,
		"gen_components.agent_id":            b.GenConfig().AgentID,
		"pick_best.judge_agent_id":           b.PickBestConfig().JudgeAgentID,
		"critique_component.critic_agent_id": b.CritiqueConfig().CriticAgentID,
	} {
		if id == "" {
			continue
		}
		if _, err := c.store.Get(ctx, KindAgent, id); err != nil {
			return invalidf("tool_config.%s: agent %q does not exist", field, id)
		}
	}
	return nil
}

// validateBodyForPublish validates a decoded body fully, including pipeline
// graph validation against the catalog.
func (c *Controller) validateBodyForPublish(ctx context.Context, kind string, body any) error {
	if kind == KindPipeline {
		cat, err := c.BuildCatalog(ctx)
		if err != nil {
			return err
		}
		if issues := ValidatePipelineBody(ctx, body.(PipelineBody), cat); len(issues) > 0 {
			return invalid("pipeline validation failed: " + summarizeIssues(issues))
		}
		return nil
	}
	if kind == KindAgent {
		if err := validateForPublish(kind, body); err != nil {
			return err
		}
		return c.checkAgentRefs(ctx, body.(AgentBody))
	}
	return validateForPublish(kind, body)
}

func summarizeIssues(issues []PipelineIssue) string {
	msg := ""
	for i, is := range issues {
		if i > 0 {
			msg += "; "
		}
		if is.NodeID != "" {
			msg += fmt.Sprintf("%s: %s", is.NodeID, is.Message)
		} else {
			msg += is.Message
		}
	}
	return msg
}

/* ---------- catalog ---------- */

func (c *Controller) getCatalog(w http.ResponseWriter, r *http.Request) error {
	cat, err := c.BuildCatalog(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, cat)
}

/* ---------- definition CRUD ---------- */

func (c *Controller) list(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	defs, err := c.store.List(r.Context(), kind)
	if err != nil {
		return err
	}
	// oldest first is the store order; apply limit/offset for large lists.
	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", 0) // 0 = all
	if offset > len(defs) {
		offset = len(defs)
	}
	defs = defs[offset:]
	if limit > 0 && limit < len(defs) {
		defs = defs[:limit]
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"items": defs, "next_cursor": nil})
}

func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

type createDefBody struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Draft       json.RawMessage `json:"draft"`
}

func (c *Controller) create(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	body, err := httpx.Decode[createDefBody](r)
	if err != nil {
		return err
	}
	if err := validateSlug(body.Slug); err != nil {
		return err
	}
	if body.Name == "" {
		return invalid("name is required")
	}
	if len(body.Draft) == 0 {
		return invalid("draft is required")
	}
	typed, err := c.decodeBodyFor(r.Context(), kind, body.Draft, true)
	if err != nil {
		return err
	}
	def, err := c.store.Create(r.Context(), kind, DefIdentity{
		Slug: body.Slug, Name: body.Name, Description: body.Description,
	}, mustMarshal(typed))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusCreated, def)
}

// validateSlug enforces ^[a-z0-9][a-z0-9-]{0,99}$.
func validateSlug(slug string) error {
	if len(slug) == 0 || len(slug) > 100 {
		return invalid("slug must be 1-100 characters")
	}
	for i := 0; i < len(slug); i++ {
		ch := slug[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		case ch == '-' && i > 0:
		default:
			return invalid("slug must match ^[a-z0-9][a-z0-9-]{0,99}$")
		}
	}
	return nil
}

func (c *Controller) get(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	def, err := c.store.Get(r.Context(), kind, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, def)
}

type updateIdentityBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// updateIdentity edits name/description (slug is immutable).
func (c *Controller) updateIdentity(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	body, err := httpx.Decode[updateIdentityBody](r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body.Name) == "" {
		return invalid("name is required")
	}
	def, err := c.store.UpdateIdentity(r.Context(), kind, chi.URLParam(r, "id"), strings.TrimSpace(body.Name), body.Description)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, def)
}

func (c *Controller) putDraft(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	// The body IS the typed body for the kind.
	raw, err := readRawBody(r)
	if err != nil {
		return err
	}
	typed, err := c.decodeBodyFor(r.Context(), kind, raw, true)
	if err != nil {
		return err
	}
	def, err := c.store.PutDraft(r.Context(), kind, chi.URLParam(r, "id"), mustMarshal(typed))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, def)
}

type publishBody struct {
	Note string `json:"note"`
}

func (c *Controller) publish(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	body, err := httpx.Decode[publishBody](r)
	if err != nil {
		return err
	}
	def, err := c.store.Get(r.Context(), kind, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	if len(def.Draft) == 0 {
		return invalid("nothing to publish: there are no unpublished changes to the body (name/description edits apply immediately and are not versioned)")
	}
	typed, err := c.decodeBodyFor(r.Context(), kind, def.Draft, true)
	if err != nil {
		return err
	}
	if err := c.validateBodyForPublish(r.Context(), kind, typed); err != nil {
		return err
	}
	def, ver, err := c.store.Publish(r.Context(), kind, def.ID, body.Note, auth.UserID(r.Context()), mustMarshal(typed))
	if err != nil {
		return err
	}
	_ = def
	return httpx.JSON(w, http.StatusOK, ver)
}

func (c *Controller) versions(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	def, err := c.store.Get(r.Context(), kind, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	vs, err := c.store.Versions(r.Context(), def.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"items": vs})
}

func (c *Controller) version(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	ver, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || ver < 1 {
		return invalid("invalid version")
	}
	def, err := c.store.Get(r.Context(), kind, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	v, err := c.store.Version(r.Context(), def.ID, ver)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, v)
}

func (c *Controller) restore(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	ver, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || ver < 1 {
		return invalid("invalid version")
	}
	def, _, err := c.store.Restore(r.Context(), kind, chi.URLParam(r, "id"), ver)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, def)
}

/* ---------- export / import ---------- */

// exportDef is one definition in a bundle.
type exportDef struct {
	Kind        string          `json:"kind"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Body        json.RawMessage `json:"body"`
	Version     int             `json:"version,omitempty"` // 0 when only a draft exists
}

type exportBundle struct {
	Version    int         `json:"version"`
	ExportedAt string      `json:"exported_at"`
	Defs       []exportDef `json:"defs"`
}

func (c *Controller) export(w http.ResponseWriter, r *http.Request) error {
	bundle := exportBundle{Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, kind := range Kinds {
		defs, err := c.store.List(r.Context(), kind)
		if err != nil {
			return err
		}
		for _, def := range defs {
			entry := exportDef{Kind: kind, Slug: def.Slug, Name: def.Name, Description: def.Description}
			if def.LatestVer != nil {
				ver, err := c.store.Version(r.Context(), def.ID, *def.LatestVer)
				if err != nil {
					return err
				}
				entry.Body, entry.Version = ver.Body, ver.Version
			} else if len(def.Draft) > 0 {
				entry.Body = def.Draft
			} else {
				continue // nothing to export
			}
			bundle.Defs = append(bundle.Defs, entry)
		}
	}
	return httpx.JSON(w, http.StatusOK, bundle)
}

type importBundleBody struct {
	Version    int         `json:"version"`
	ExportedAt string      `json:"exported_at"`
	Defs       []exportDef `json:"defs"`
}

type importEntryError struct {
	Slug  string `json:"slug"`
	Error string `json:"error"`
}

type importResult struct {
	Imported int                `json:"imported"`
	Skipped  int                `json:"skipped"`
	Errors   []importEntryError `json:"errors"`
}

func (c *Controller) importBundle(w http.ResponseWriter, r *http.Request) error {
	bundle, err := httpx.Decode[importBundleBody](r)
	if err != nil {
		return err
	}
	user := auth.UserID(r.Context())
	res := importResult{Errors: []importEntryError{}}
	seen := map[string]bool{}
	for _, entry := range bundle.Defs {
		slugKey := entry.Kind + "/" + entry.Slug
		switch {
		case !ValidKind(entry.Kind):
			res.Skipped++
			continue
		case entry.Slug == "" || len(entry.Body) == 0:
			res.Skipped++
			continue
		case seen[slugKey]:
			res.Errors = append(res.Errors, importEntryError{Slug: entry.Slug, Error: "duplicate entry in bundle"})
			continue
		}
		seen[slugKey] = true

		if err := c.importOne(r.Context(), user, entry); err != nil {
			res.Errors = append(res.Errors, importEntryError{Slug: entry.Slug, Error: err.Error()})
			continue
		}
		res.Imported++
	}
	return httpx.JSON(w, http.StatusOK, res)
}

// importOne validates a bundle entry and creates + publishes it (or publishes
// a new version and overwrites the draft of an existing def).
func (c *Controller) importOne(ctx context.Context, user string, entry exportDef) error {
	body, err := BodyForKind(entry.Kind, entry.Body)
	if err != nil {
		return err
	}
	if err := validateForPublish(entry.Kind, body); err != nil {
		return err
	}
	if entry.Kind == KindAgent {
		if err := c.checkAgentRefs(ctx, body.(AgentBody)); err != nil {
			return err
		}
	}
	if entry.Kind == KindPipeline {
		if err := c.validateBodyForPublish(ctx, entry.Kind, body); err != nil {
			return err
		}
	}

	existing, err := c.store.GetBySlug(ctx, entry.Kind, entry.Slug)
	if err == nil {
		// Existing: publish as a new version and overwrite the draft.
		if _, _, err := c.store.Publish(ctx, entry.Kind, existing.ID, "imported", user, entry.Body); err != nil {
			return err
		}
		if _, err := c.store.PutDraft(ctx, entry.Kind, existing.ID, entry.Body); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// Missing: create + publish.
	def, err := c.store.Create(ctx, entry.Kind, DefIdentity{
		Slug: entry.Slug, Name: entry.Name, Description: entry.Description,
	}, entry.Body)
	if err != nil {
		return err
	}
	if _, _, err := c.store.Publish(ctx, entry.Kind, def.ID, "imported", user, entry.Body); err != nil {
		return err
	}
	return nil
}

/* ---------- misc ---------- */

// readRawBody reads the request body as raw JSON (1MB cap, malformed → invalid).
func readRawBody(r *http.Request) (json.RawMessage, error) {
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, httpx.MaxBodyBytes))
	if err != nil {
		return nil, invalid("malformed JSON body").Wrap(err)
	}
	return json.RawMessage(raw), nil
}

// mustMarshal canonicalizes a typed body (it always marshals).
func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("studio: marshal body: %v", err))
	}
	return raw
}
