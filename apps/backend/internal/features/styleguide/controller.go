package styleguide

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Module registers the model, the service and /styleguides.
var Module = fx.Options(
	dbx.AsModel(&models.Styleguide{}),
	fx.Provide(newServiceFx),
	httpx.AsController(NewController),
	fx.Invoke(func(*Service) {}),
)

func newServiceFx(lc fx.Lifecycle, db *gorm.DB, bl *blobs.Store, defs *studio.Store, rt *agentrun.Runtime, log *slog.Logger) *Service {
	s := NewService(db, bl, defs, rt, log)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			n, err := s.Recover(ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				log.Warn("styleguide: marked unfinished generations failed", "count", n)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return s.Close(stopCtx)
		},
	})
	return s
}

// maxUploadBytes bounds a multipart upload (all images plus the text fields).
const maxUploadBytes = MaxImages*MaxImageBytes + 1<<20

// Controller serves /styleguides (any authenticated user, owner scoped).
type Controller struct {
	svc *Service
	mw  *auth.Middleware
}

// NewController builds the controller.
func NewController(s *Service, mw *auth.Middleware) *Controller {
	return &Controller{svc: s, mw: mw}
}

// Routes registers the routes.
func (c *Controller) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(c.mw.Required)
		r.Get("/styleguides", httpx.Handle(c.list))
		r.Post("/styleguides", httpx.Handle(c.create))
		r.Get("/styleguides/{id}", httpx.Handle(c.get))
		r.Patch("/styleguides/{id}", httpx.Handle(c.update))
		r.Delete("/styleguides/{id}", httpx.Handle(c.delete))
		r.Post("/styleguides/{id}/regenerate", httpx.Handle(c.regenerate))
		r.Get("/styleguides/{id}/images/{n}", httpx.Handle(c.image))
	})
}

// View is a style guide on the wire (apps/frontend/src/api/styleguides.ts).
type View struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	Notes      string    `json:"notes"`
	Markdown   string    `json:"markdown"`
	ImageCount int       `json:"image_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func viewOf(sg *models.Styleguide) View {
	name := sg.Name
	if name == "" {
		name = defaultName
	}
	return View{
		ID: sg.ID, Name: name, Status: sg.Status, Error: sg.Error, Notes: sg.Notes,
		Markdown: sg.Markdown, ImageCount: len(sg.Images), CreatedAt: sg.CreatedAt, UpdatedAt: sg.UpdatedAt,
	}
}

func viewerOf(r *http.Request) Viewer {
	v := Viewer{UserID: auth.UserID(r.Context())}
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		v.Admin = claims.Role == "admin"
	}
	return v
}

func (c *Controller) list(w http.ResponseWriter, r *http.Request) error {
	items, err := c.svc.List(r.Context(), viewerOf(r))
	if err != nil {
		return err
	}
	out := make([]View, len(items))
	for i := range items {
		out[i] = viewOf(&items[i])
	}
	return httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (c *Controller) get(w http.ResponseWriter, r *http.Request) error {
	sg, err := c.svc.Get(r.Context(), viewerOf(r), chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, viewOf(sg))
}

// create takes multipart/form-data: `images` (1–4 files), `name?`, `notes?`.
// 201 with the guide in status generating.
func (c *Controller) create(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return apperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", "the images are too large")
		}
		return apperr.Invalid("expected multipart/form-data with images", nil)
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	var images []Image
	for _, fh := range r.MultipartForm.File["images"] {
		if fh.Size > MaxImageBytes {
			return apperr.Invalid("invalid image", map[string]string{"images": fh.Filename + " is larger than 8 MB"})
		}
		f, err := fh.Open()
		if err != nil {
			return apperr.Invalid("invalid image", map[string]string{"images": "could not read " + fh.Filename})
		}
		data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
		_ = f.Close()
		if err != nil || len(data) > MaxImageBytes {
			return apperr.Invalid("invalid image", map[string]string{"images": "could not read " + fh.Filename})
		}
		mime := http.DetectContentType(data)
		switch mime {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			return apperr.Invalid("invalid image", map[string]string{"images": fh.Filename + " is not a PNG, JPEG, WebP or GIF image"})
		}
		images = append(images, Image{Data: data, MIME: mime})
	}
	sg, err := c.svc.Create(r.Context(), viewerOf(r), r.FormValue("name"), r.FormValue("notes"), images)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusCreated, viewOf(sg))
}

// updateRequest is PATCH /styleguides/{id}; absent fields are kept.
type updateRequest struct {
	Name     *string `json:"name"`
	Markdown *string `json:"markdown"`
}

func (c *Controller) update(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[updateRequest](r)
	if err != nil {
		return err
	}
	sg, err := c.svc.Update(r.Context(), viewerOf(r), chi.URLParam(r, "id"), req.Name, req.Markdown)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, viewOf(sg))
}

func (c *Controller) delete(w http.ResponseWriter, r *http.Request) error {
	if err := c.svc.Delete(r.Context(), viewerOf(r), chi.URLParam(r, "id")); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusNoContent, nil)
}

func (c *Controller) regenerate(w http.ResponseWriter, r *http.Request) error {
	sg, err := c.svc.Regenerate(r.Context(), viewerOf(r), chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, viewOf(sg))
}

func (c *Controller) image(w http.ResponseWriter, r *http.Request) error {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil {
		return apperr.NotFound("image not found")
	}
	b, err := c.svc.Image(r.Context(), viewerOf(r), chi.URLParam(r, "id"), n)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", b.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, err = w.Write(b.Data)
	return err
}
