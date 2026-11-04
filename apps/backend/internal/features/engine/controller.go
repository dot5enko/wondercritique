package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Controller serves the runs API (mounted at /api/v1, any authenticated user;
// runs are scoped to their owner, admins see all).
type Controller struct {
	engine *Engine
	mw     *auth.Middleware
}

// NewController builds the runs controller.
func NewController(e *Engine, mw *auth.Middleware) *Controller {
	return &Controller{engine: e, mw: mw}
}

// Routes registers the runs and artifact routes.
func (c *Controller) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(c.mw.Required)
		r.Post("/runs", httpx.Handle(c.start))
		r.Get("/runs", httpx.Handle(c.list))
		r.Get("/runs/{id}", httpx.Handle(c.get))
		r.Get("/runs/{id}/events", httpx.Handle(c.events))
		r.Post("/runs/{id}/cancel", httpx.Handle(c.cancel))
		r.Get("/artifacts/{id}/content", httpx.Handle(c.artifactContent))
	})
}

func viewerOf(r *http.Request) Viewer {
	v := Viewer{UserID: auth.UserID(r.Context())}
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		v.Admin = claims.Role == "admin"
	}
	return v
}

type startRequest struct {
	PipelineVersionID string                     `json:"pipeline_version_id"`
	PipelineID        string                     `json:"pipeline_id"`
	// Agent (slug or id) runs one chat agent instead of a pipeline.
	Agent    string `json:"agent"`
	ThreadID string `json:"thread_id"`
	IsTest            bool                       `json:"is_test"`
	Input             map[string]json.RawMessage `json:"input" validate:"required"`
	// ContextPaths are selected canvas files/folders the message is about.
	ContextPaths []string `json:"context_paths"`
}

// start answers 201 `{id, thread_id, status}`; the run executes in the
// background (follow it via /runs/{id}/events).
func (c *Controller) start(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[startRequest](r)
	if err != nil {
		return err
	}
	run, err := c.engine.Start(r.Context(), StartParams{
		Viewer:            viewerOf(r),
		PipelineVersionID: req.PipelineVersionID,
		PipelineID:        req.PipelineID,
		Agent:             req.Agent,
		ThreadID:          req.ThreadID,
		IsTest:            req.IsTest,
		Input:             req.Input,
		ContextPaths:      req.ContextPaths,
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusCreated, map[string]any{"id": run.ID, "thread_id": run.ThreadID, "status": run.Status})
}

func (c *Controller) list(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	p := ListParams{ThreadID: q.Get("filter[thread_id]")}
	var err error
	if p.Limit, err = intParam(q.Get("limit")); err != nil {
		return apperr.Invalid("invalid limit", map[string]string{"limit": "must be a number"})
	}
	if p.Offset, err = intParam(q.Get("offset")); err != nil {
		return apperr.Invalid("invalid offset", map[string]string{"offset": "must be a number"})
	}
	items, err := c.engine.ListRuns(r.Context(), viewerOf(r), p)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func intParam(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

func (c *Controller) get(w http.ResponseWriter, r *http.Request) error {
	d, err := c.engine.RunDetail(r.Context(), chi.URLParam(r, "id"), viewerOf(r))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

func (c *Controller) cancel(w http.ResponseWriter, r *http.Request) error {
	if err := c.engine.Cancel(r.Context(), chi.URLParam(r, "id"), viewerOf(r)); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusNoContent, nil)
}

// artifactContent serves the blob of an artifact with its stored MIME type.
// The content is untrusted model output: it is served inert (sandboxed CSP,
// no sniffing) and the UI only ever shows it in a sandboxed iframe.
func (c *Controller) artifactContent(w http.ResponseWriter, r *http.Request) error {
	b, err := c.engine.ArtifactBlob(r.Context(), chi.URLParam(r, "id"), viewerOf(r))
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", b.MIME)
	h.Set("Content-Length", strconv.Itoa(len(b.Data)))
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	_, err = w.Write(b.Data)
	return err
}

/* ---------- SSE ---------- */

const heartbeat = 15 * time.Second

func writeFrame(w http.ResponseWriter, fl http.Flusher, ev busEvent) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, ev.Data); err != nil {
		return err
	}
	fl.Flush()
	return nil
}

// events streams a run's events: the replayed history first, then live events,
// ending after the terminal event. A run no longer known to the bus answers
// with its terminal state synthesized from the database.
func (c *Controller) events(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	run, err := c.engine.GetRun(r.Context(), id, viewerOf(r))
	if err != nil {
		return err
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		return apperr.Unavailable("streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	replay, sub, live := c.engine.bus.subscribe(id)
	if !live {
		_ = writeFrame(w, fl, c.engine.terminalFromRun(r.Context(), run))
		return nil
	}
	defer c.engine.bus.unsubscribe(id, sub)

	for _, ev := range replay {
		if err := writeFrame(w, fl, ev); err != nil {
			return nil
		}
		if ev.terminal() {
			return nil
		}
	}
	if sub == nil {
		return nil
	}

	hb := time.NewTicker(heartbeat)
	defer hb.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			fl.Flush()
		case ev, ok := <-sub.ch:
			if !ok {
				return nil
			}
			if err := writeFrame(w, fl, ev); err != nil || ev.terminal() {
				return nil
			}
		}
	}
}

// terminalFromRun builds the terminal event of a run from its stored state.
func (e *Engine) terminalFromRun(ctx context.Context, run *models.Run) busEvent {
	fields := map[string]any{"run_id": run.ID}
	name := "run_failed"
	if run.Status == models.RunCompleted {
		name = "run_completed"
		fields["usage"] = run.Usage
		if run.BestArtifactID != nil {
			fields["best_artifact_id"] = *run.BestArtifactID
		}
		if run.Cost != nil {
			fields["cost"] = *run.Cost
		}
		if e.Canvas != nil {
			if paths, err := e.Canvas.PathsOfRun(ctx, run.ID); err == nil {
				fields["canvas_paths"] = paths
			}
		}
		if run.Reply != "" {
			fields["reply"] = run.Reply
		}
	} else {
		msg := run.Error
		if msg == "" {
			msg = "The run is no longer active (" + run.Status + ")."
		}
		fields["error"] = msg
		fields["status"] = run.Status
	}
	fields["event"] = name
	data, _ := json.Marshal(fields)
	return busEvent{Name: name, Data: data}
}
