package canvas

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Module registers the canvas models, the store and the HTTP API.
var Module = fx.Options(
	dbx.AsModel(&models.CanvasFile{}),
	dbx.AsModel(&models.CanvasState{}),
	fx.Provide(NewStore),
	httpx.AsController(NewController),
)

// Controller serves /threads/{id}/canvas* (any authenticated user; owner
// scoped like runs, admins see all).
type Controller struct {
	store    *Store
	mw       *auth.Middleware
	renderer render.Renderer // PNG export
}

// NewController builds the canvas controller.
func NewController(s *Store, mw *auth.Middleware, rd render.Renderer) *Controller {
	return &Controller{store: s, mw: mw, renderer: rd}
}

// Routes registers the canvas routes.
func (c *Controller) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(c.mw.Required)
		r.Get("/threads/{id}/canvas", httpx.Handle(c.get))
		r.Put("/threads/{id}/canvas/layout", httpx.Handle(c.putLayout))
		r.Post("/threads/{id}/canvas/delete", httpx.Handle(c.delete))
		r.Post("/threads/{id}/canvas/restore", httpx.Handle(c.restore))
		r.Post("/threads/{id}/canvas/move", httpx.Handle(c.move))
		r.Post("/threads/{id}/canvas/export", httpx.Handle(c.export))
	})
}

func viewerOf(r *http.Request) Viewer {
	v := Viewer{UserID: auth.UserID(r.Context())}
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		v.Admin = claims.Role == "admin"
	}
	return v
}

// thread checks access and returns the thread id and its owner.
func (c *Controller) thread(r *http.Request) (string, string, error) {
	id := chi.URLParam(r, "id")
	owner, err := c.store.Access(r.Context(), id, viewerOf(r))
	return id, owner, err
}

// FileView is one canvas file on the wire (apps/frontend/src/api/canvas.ts).
type FileView struct {
	Path       string    `json:"path"`
	ArtifactID string    `json:"artifact_id"`
	RunID      string    `json:"run_id"`
	SourcePath string    `json:"source_path"`
	Score      *float64  `json:"score"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}

// CanvasView is GET /threads/{id}/canvas.
type CanvasView struct {
	ThreadID     string              `json:"thread_id"`
	Files        []FileView          `json:"files"`
	Layout       models.CanvasLayout `json:"layout"`
	PendingNotes []models.CanvasNote `json:"pending_notes"`
}

// layoutOut keeps empty maps serializing as {}.
func layoutOut(l models.CanvasLayout) models.CanvasLayout {
	if l.Pos == nil {
		l.Pos = map[string]models.Point{}
	}
	if l.Scale == nil {
		l.Scale = map[string]float64{}
	}
	if l.Dir == nil {
		l.Dir = map[string]string{}
	}
	return l
}

func (c *Controller) get(w http.ResponseWriter, r *http.Request) error {
	id, _, err := c.thread(r)
	if err != nil {
		return err
	}
	files, err := c.store.Files(r.Context(), id)
	if err != nil {
		return err
	}
	st, err := c.store.State(r.Context(), id)
	if err != nil {
		return err
	}
	out := CanvasView{ThreadID: id, Files: make([]FileView, 0, len(files)), Layout: layoutOut(st.Layout), PendingNotes: st.Notes}
	if out.PendingNotes == nil {
		out.PendingNotes = []models.CanvasNote{}
	}
	for _, f := range files {
		out.Files = append(out.Files, FileView{
			Path: f.Path, ArtifactID: f.ArtifactID, RunID: f.RunID, SourcePath: f.SourcePath,
			Score: f.Score, Size: f.Size, CreatedAt: f.CreatedAt,
		})
	}
	return httpx.JSON(w, http.StatusOK, out)
}

type layoutRequest struct {
	Layout models.CanvasLayout `json:"layout"`
}

func (c *Controller) putLayout(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[layoutRequest](r)
	if err != nil {
		return err
	}
	id, owner, err := c.thread(r)
	if err != nil {
		return err
	}
	l, err := c.store.PutLayout(r.Context(), id, owner, req.Layout)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"layout": layoutOut(l)})
}

type deleteRequest struct {
	Paths []string `json:"paths" validate:"required,min=1,max=200"`
}

func (c *Controller) delete(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[deleteRequest](r)
	if err != nil {
		return err
	}
	id, owner, err := c.thread(r)
	if err != nil {
		return err
	}
	op, deleted, err := c.store.Delete(r.Context(), id, owner, req.Paths)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"op_id": op, "deleted": deleted})
}

type restoreRequest struct {
	OpID string `json:"op_id" validate:"required"`
}

func (c *Controller) restore(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[restoreRequest](r)
	if err != nil {
		return err
	}
	id, owner, err := c.thread(r)
	if err != nil {
		return err
	}
	restored, err := c.store.Restore(r.Context(), id, owner, req.OpID)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"restored": restored})
}

type moveRequest struct {
	From string `json:"from" validate:"required"`
	To   string `json:"to" validate:"required"`
}

func (c *Controller) move(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[moveRequest](r)
	if err != nil {
		return err
	}
	id, owner, err := c.thread(r)
	if err != nil {
		return err
	}
	moved, err := c.store.Move(r.Context(), id, owner, req.From, req.To)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"moved": moved})
}
