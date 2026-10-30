// Package messages exposes thread messages through the generic CRUD layer at /api/v1/messages.
// All operations require authentication; every row is owned by the creating user and queries
// are scoped to it.
package messages

import (
	"context"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/crud"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/models"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
	"gorm.io/gorm"
)

var Module = fx.Module("messages",
	dbx.AsModel(&models.Message{}),
	httpx.AsController(NewController),
)

type Controller struct {
	crud *crud.Registry
	mw   *auth.Middleware
}

func NewController(reg *crud.Registry, mw *auth.Middleware) *Controller {
	return &Controller{crud: reg, mw: mw}
}

func (c *Controller) Routes(r chi.Router) {
	api := c.crud.On(r)
	crud.NewForType[models.Message](api, "/messages",
		crud.Use(c.mw.Required),
		crud.Scope[models.Message](func(ctx context.Context, q *gorm.DB) *gorm.DB {
			return q.Where("owner_id = ?", auth.UserID(ctx))
		}),
		crud.WithHooks(crud.Hooks[models.Message]{
			BeforeCreate: func(ctx context.Context, m *models.Message) error {
				m.OwnerID = auth.UserID(ctx)
				return nil
			},
		}),
	)
}
