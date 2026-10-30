// Package health exposes liveness (/healthz) and readiness (/readyz) probes at the root path.
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

var Module = fx.Module("health", httpx.AsRootController(NewController))

type Controller struct{ db *gorm.DB }

func NewController(db *gorm.DB) *Controller { return &Controller{db: db} }

func (c *Controller) Routes(r chi.Router) {
	r.Get("/healthz", httpx.Handle(c.live))
	r.Get("/readyz", httpx.Handle(c.ready))
}

type status struct {
	Status string `json:"status"`
}

func (c *Controller) live(w http.ResponseWriter, _ *http.Request) error {
	return httpx.JSON(w, http.StatusOK, status{"ok"})
}

func (c *Controller) ready(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	sqlDB, err := dbx.SQLDB(c.db)
	if err == nil {
		err = sqlDB.PingContext(ctx)
	}
	if err != nil {
		return apperr.Unavailable("database unavailable").Wrap(err)
	}
	return httpx.JSON(w, http.StatusOK, status{"ok"})
}
