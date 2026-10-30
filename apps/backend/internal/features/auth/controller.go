package auth

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

// Controller serves /api/v1/auth.
type Controller struct {
	svc     *Service
	mw      *Middleware
	limiter *ipLimiter
}

func NewController(svc *Service, mw *Middleware, rl RateLimit, now Clock) *Controller {
	return &Controller{svc: svc, mw: mw, limiter: newIPLimiter(rl, now)}
}

func (c *Controller) Routes(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(c.limiter.middleware)
			r.Post("/{provider}/start", handle(c.start))
			r.Post("/{provider}/verify", handle(c.verify))
		})
		r.Post("/refresh", handle(c.refresh))
		r.Post("/logout", handle(c.logout))
		r.With(c.mw.Required).Get("/me", handle(c.me))
	})
}

func (c *Controller) start(w http.ResponseWriter, r *http.Request) error {
	body, err := rawBody(w, r)
	if err != nil {
		return err
	}
	res, err := c.svc.Start(r.Context(), chi.URLParam(r, "provider"), body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, res)
}

func (c *Controller) verify(w http.ResponseWriter, r *http.Request) error {
	body, err := rawBody(w, r)
	if err != nil {
		return err
	}
	pair, err := c.svc.Verify(r.Context(), chi.URLParam(r, "provider"), body)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, pair)
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token" validate:"required,max=128"`
}

func (c *Controller) refresh(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[refreshReq](r)
	if err != nil {
		return err
	}
	pair, err := c.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, pair)
}

func (c *Controller) logout(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[refreshReq](r)
	if err != nil {
		return err
	}
	if err := c.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusNoContent, nil)
}

func (c *Controller) me(w http.ResponseWriter, r *http.Request) error {
	u, err := c.svc.User(r.Context(), UserID(r.Context()))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, u)
}

// rawBody reads a provider request body (max httpx.MaxBodyBytes); it must be a JSON object.
func rawBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, apperr.New(http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		}
		return nil, apperr.Invalid("could not read request body", nil).Wrap(err)
	}
	if len(b) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(b) {
		return nil, apperr.Invalid("malformed JSON", nil)
	}
	return b, nil
}

// handle is httpx.Handle plus a Retry-After header for rate-limit errors.
func handle(fn httpx.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeError(w, r, err)
		}
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ra retryAfter
	if errors.As(err, &ra) {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(ra.d.Seconds()))))
	}
	httpx.WriteError(w, r, err)
}
