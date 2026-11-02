// HTTP surface of the agent runtime: the studio preview/try endpoints (admin),
// the pipeline validate route, and the generic chat mount replacing the demo
// critic (plan §4.3, §6).
package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/httpapi"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

// Controller serves the agent action routes (preview/try/validate) and the
// generic chat mount. Chat endpoints require auth but not admin.
type Controller struct {
	runtime *Runtime
	store   *studio.Store
	mw      *auth.Middleware
}

// NewController builds the agent runtime controller.
func NewController(runtime *Runtime, store *studio.Store, mw *auth.Middleware) *Controller {
	return &Controller{runtime: runtime, store: store, mw: mw}
}

// StudioRoutes registers the studio action routes (plural segments by
// frontend contract). Mounted by the studio controller inside its single
// `/studio` subrouter, which already applies auth + admin middleware.
func (c *Controller) StudioRoutes(r chi.Router) {
	r.Post("/agents/{id}/preview", httpx.Handle(c.preview))
	r.Post("/agents/{id}/try", httpx.Handle(c.tryRun))
	r.Post("/pipelines/{id}/validate", httpx.Handle(c.validatePipeline))
}

// Routes registers the runtime routes on r (mounted at /api/v1).
func (c *Controller) Routes(r chi.Router) {
	// Chat with any published agent (any authenticated user).
	r.Route("/agents", func(r chi.Router) {
		r.Use(c.mw.Required)
		r.Mount("/{slug}", http.HandlerFunc(c.chat))
	})
}

/* ---------- preview ---------- */

type sampleInput struct {
	Input map[string]any `json:"input"`
}

type agentPreview struct {
	System string `json:"system"`
	Input  string `json:"input"`
}

// preview assembles the final system + input texts for sample data (no model call).
func (c *Controller) preview(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[sampleInput](r)
	if err != nil {
		return err
	}
	res, err := c.runtime.resolveAgent(r.Context(), chi.URLParam(r, "id"), true)
	if err != nil {
		return err
	}
	system, inputText := c.assemble(r.Context(), res.Body, body.Input, true)
	return httpx.JSON(w, http.StatusOK, agentPreview{System: system, Input: inputText})
}

// assemble renders the assembled system prompt and input text.
func (c *Controller) assemble(ctx context.Context, body studio.AgentBody, samples map[string]any, preview bool) (string, string) {
	ruleGroups, skills, _ := c.runtime.loadRefs(ctx, body)
	promptSkills := make([]PromptSkill, len(skills))
	for i, sk := range skills {
		promptSkills[i] = PromptSkill{Slug: sk.slug, Manifest: sk.manifest, Files: len(sk.files) - 1}
	}
	system := AssembleSystem(body.SystemPrompt, ruleGroups, promptSkills, body.OutputContract)
	msg := BuildInput(body.InputTemplate, samples, preview)
	return system, msg.Text()
}

/* ---------- try ---------- */

// tryRun streams one run of the agent on sample input (draft allowed) as SSE,
// framed exactly like agentkit/httpapi (event lines, heartbeats, final done).
func (c *Controller) tryRun(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[sampleInput](r)
	if err != nil {
		return err
	}
	res, err := c.runtime.resolveAgent(r.Context(), chi.URLParam(r, "id"), true)
	if err != nil {
		return err
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		return apperr.Unavailable("streaming unsupported")
	}

	// An EPHEMERAL in-memory store per try run: nothing persists.
	name := "try:" + res.Def.ID
	memAgent, err := c.runtime.buildAgent(r.Context(), res, name, memory.New())
	if err != nil {
		return err
	}

	sess, err := memAgent.NewSession(r.Context(), agentkit.SessionOptions{Title: name})
	if err != nil {
		return err
	}
	c.runtime.EphemeralSession(sess.ID, name)
	msg := BuildInput(res.Body.InputTemplate, body.Input, false)

	streamSSE(w, fl, r.Context(), memAgent, sess.ID, []llm.Message{msg})
	return nil
}

/* ---------- pipeline validate ---------- */

type pipelineValidation struct {
	OK     bool                   `json:"ok"`
	Issues []studio.PipelineIssue `json:"issues"`
}

// validatePipeline runs full graph validation on the draft (or latest
// published) pipeline body via the studio package's ValidatePipelineBody.
func (c *Controller) validatePipeline(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	def, err := c.store.Get(r.Context(), studio.KindPipeline, id)
	if err != nil {
		return err
	}
	raw := def.Draft
	if len(raw) == 0 || string(raw) == "null" {
		if def.LatestVer == nil {
			return apperr.Invalid("no draft and no published version to validate", nil)
		}
		ver, err := c.store.Version(r.Context(), def.ID, *def.LatestVer)
		if err != nil {
			return err
		}
		raw = ver.Body
	}
	body, err := studio.DecodeBody[studio.PipelineBody](raw)
	if err != nil {
		return err
	}
	cat, err := studio.BuildCatalogData(r.Context(), c.store, c.runtime.llm)
	if err != nil {
		return err
	}
	issues := studio.ValidatePipelineBody(r.Context(), body, cat)
	return httpx.JSON(w, http.StatusOK, pipelineValidation{OK: len(issues) == 0, Issues: issues})
}

/* ---------- chat mount ---------- */

// chat serves the agentkit httpapi for one published agent. The inner handler
// is a Go 1.22 ServeMux with relative patterns, so the chi RoutePath is copied
// into req.URL.Path before serving (the deleted demo controller's trick).
func (c *Controller) chat(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	res, err := c.runtime.resolveBySlug(r.Context(), slug)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	agent, err := c.runtime.Agent(r.Context(), res, res.Def.Slug)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	handler := httpapi.New(agent, httpapi.Options{
		SessionOwner: func(req *http.Request) (string, error) {
			id := auth.UserID(req.Context())
			if id == "" {
				return "", errors.New("agentrun: no authenticated user")
			}
			return id, nil
		},
		Logger: c.runtime.logWriter(),
	})

	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		r.URL.Path = rctx.RoutePath
	}
	handler.ServeHTTP(w, r)
}

/* ---------- SSE ---------- */

// streamSSE executes a run and writes each agentkit event as an SSE frame,
// mirroring httpapi.streamRun: heartbeats, final `event: done` with `{}`,
// client disconnect cancels the run.
func streamSSE(w http.ResponseWriter, fl http.Flusher, ctx context.Context, a *agentkit.Agent, sessionID string, msgs []llm.Message) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	events := make(chan agentkit.Event, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		for ev, err := range a.Run(ctx, sessionID, msgs...) {
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return // the run_failed event was delivered above
			}
		}
	}()

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				break loop
			}
			fl.Flush()
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				break loop
			}
			fl.Flush()
		}
	}
	<-done // let the run finish persisting state
	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	fl.Flush()
}
