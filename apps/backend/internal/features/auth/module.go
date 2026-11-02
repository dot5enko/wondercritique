// Package auth implements login through pluggable providers (default: 6-digit email code), JWT access
// tokens, rotating refresh tokens, and the HTTP middleware/helpers other features use to authenticate.
package auth

import (
	"time"

	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth/mailer"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Module provides *Service, *Tokens, *Middleware, mailer.Mailer, the email-code provider and the /auth routes.
// Tests may fx.Decorate Clock, mailer.Mailer and RateLimit.
var Module = fx.Module("auth",
	dbx.AsModel(&models.User{}),
	dbx.AsModel(&UserIdentity{}),
	dbx.AsModel(&AuthCode{}),
	dbx.AsModel(&RefreshToken{}),
	fx.Provide(
		func() Clock { return time.Now },
		func() RateLimit { return DefaultRateLimit },
		newKeys,
		newTokens,
		NewMiddleware,
		NewService,
		mailer.New,
	),
	fx.Invoke(bootstrapAdmins), // runs after dbx's migrate invoke (dbx.Module is registered first)
	AsProvider(newEmailCodeProvider),
	httpx.AsController(NewController),
)
