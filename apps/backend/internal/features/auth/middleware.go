package auth

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Middleware authenticates requests with a Bearer access token. It is an fx-provided value (not package-level
// funcs) because verifying needs the signing key; inject *auth.Middleware and use m.Required / m.Optional.
// Reading the result (UserID, ClaimsFrom, Role) only needs the context, so those are package-level.
type Middleware struct{ tokens *Tokens }

func NewMiddleware(t *Tokens) *Middleware { return &Middleware{tokens: t} }

// IssueAccessToken signs an access token for u; useful for tests and token minting outside HTTP.
func (m *Middleware) IssueAccessToken(u *models.User) (string, error) { return m.tokens.IssueAccess(u) }

// NewTestMiddleware builds a Middleware that verifies tokens signed with secret. It exists so
// other packages can exercise middleware-protected routes in tests.
func NewTestMiddleware(secret, issuer string) *Middleware {
	log := slog.New(slog.DiscardHandler)
	cfg := &config.Config{Env: "dev", JWTSecret: secret, JWTIssuer: issuer}
	k, err := newKeys(cfg, log)
	if err != nil {
		panic("auth: NewTestMiddleware: " + err.Error())
	}
	return NewMiddleware(newTokens(k, cfg, func() time.Time { return time.Now() }))
}

// Required rejects requests without a valid access token with 401.
func (m *Middleware) Required(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.authenticate(r)
		if err == nil && c == nil {
			err = apperr.Unauthorized("missing access token")
		}
		if err != nil {
			unauthorized(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), c)))
	})
}

// Optional lets anonymous requests through, but a present and invalid token is still rejected with 401
// (so clients notice an expired token instead of silently becoming anonymous).
func (m *Middleware) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.authenticate(r)
		if err != nil {
			unauthorized(w, r, err)
			return
		}
		if c != nil {
			r = r.WithContext(WithClaims(r.Context(), c))
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate returns (nil, nil) when no Authorization header is present.
func (m *Middleware) authenticate(r *http.Request) (*Claims, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, nil
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return nil, apperr.Unauthorized("malformed Authorization header")
	}
	c, err := m.tokens.ParseAccess(strings.TrimSpace(token))
	if err != nil {
		return nil, apperr.Unauthorized("invalid or expired access token").Wrap(err)
	}
	return c, nil
}

func unauthorized(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("WWW-Authenticate", `Bearer`)
	httpx.WriteError(w, r, err)
}

// Role allows only callers whose role is one of roles: 401 without claims, 403 otherwise.
// Place it after Required (or Optional).
func Role(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFrom(r.Context())
			if !ok {
				unauthorized(w, r, apperr.Unauthorized("authentication required"))
				return
			}
			if !slices.Contains(roles, c.Role) {
				httpx.WriteError(w, r, apperr.Forbidden("insufficient role"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type claimsKey struct{}

// WithClaims returns ctx carrying c (used by the middleware; handy in tests and background jobs).
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom returns the authenticated caller's claims.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(*Claims)
	return c, ok && c != nil
}

// UserID returns the authenticated user's id, or "" for anonymous requests.
func UserID(ctx context.Context) string {
	if c, ok := ClaimsFrom(ctx); ok {
		return c.Subject
	}
	return ""
}
