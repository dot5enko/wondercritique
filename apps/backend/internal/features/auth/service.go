package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.uber.org/fx"
	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

var errBadRefresh = apperr.Unauthorized("invalid refresh token")

// TokenPair is returned by verify and refresh.
type TokenPair struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int          `json:"expires_in"` // access token lifetime, seconds
	User         *models.User `json:"user"`
}

// Service implements login, token refresh and logout on top of the registered providers.
type Service struct {
	db        *gorm.DB
	tokens    *Tokens
	now       Clock
	log       *slog.Logger
	providers map[string]Provider
}

// ServiceParams are the dependencies of NewService.
type ServiceParams struct {
	fx.In
	DB        *gorm.DB
	Tokens    *Tokens
	Clock     Clock
	Log       *slog.Logger
	Providers []Provider `group:"auth_providers"`
}

func NewService(p ServiceParams) (*Service, error) {
	s := &Service{db: p.DB, tokens: p.Tokens, now: p.Clock, log: p.Log, providers: map[string]Provider{}}
	for _, pr := range p.Providers {
		if _, dup := s.providers[pr.Name()]; dup {
			return nil, fmt.Errorf("auth: duplicate provider %q", pr.Name())
		}
		s.providers[pr.Name()] = pr
	}
	return s, nil
}

func (s *Service) provider(name string) (Provider, error) {
	p, ok := s.providers[name]
	if !ok {
		return nil, apperr.NotFound("unknown auth provider")
	}
	return p, nil
}

// Start runs the provider's first step.
func (s *Service) Start(ctx context.Context, provider string, req json.RawMessage) (StartResult, error) {
	p, err := s.provider(provider)
	if err != nil {
		return nil, err
	}
	return p.Start(ctx, req)
}

// Verify completes a provider login, resolves or creates the user and issues tokens.
func (s *Service) Verify(ctx context.Context, provider string, req json.RawMessage) (*TokenPair, error) {
	p, err := s.provider(provider)
	if err != nil {
		return nil, err
	}
	// Not in a tx: providers persist failed attempts, which must not be rolled back.
	id, err := p.Verify(ctx, req)
	if err != nil {
		return nil, err
	}
	id.Email = NormalizeEmail(id.Email)
	if id.Provider == "" || id.Subject == "" || id.Email == "" {
		return nil, apperr.Internal(fmt.Errorf("auth: provider %q returned incomplete identity %+v", provider, id))
	}

	var pair *TokenPair
	err = dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		u, err := s.resolveUser(ctx, id)
		if err != nil {
			return err
		}
		pair, _, err = s.issue(ctx, u)
		return err
	})
	return pair, err
}

// resolveUser finds the user by identity, else links the identity to the user with that email, else creates both.
func (s *Service) resolveUser(ctx context.Context, id Identity) (*models.User, error) {
	db := dbx.Conn(ctx, s.db)
	var ident UserIdentity
	err := db.Where("provider = ? AND subject = ?", id.Provider, id.Subject).Take(&ident).Error
	if err == nil {
		var u models.User
		if err := db.Take(&u, "id = ?", ident.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperr.Forbidden("account is disabled")
			}
			return nil, err
		}
		return &u, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Unscoped: the unique email index also covers soft-deleted users, which must not be recreated.
	var u models.User
	err = db.Unscoped().Where("email = ?", id.Email).Take(&u).Error
	switch {
	case err == nil && u.DeletedAt.Valid:
		return nil, apperr.Forbidden("account is disabled")
	case errors.Is(err, gorm.ErrRecordNotFound):
		u = models.User{Email: id.Email, Name: id.Name, Role: models.RoleUser}
		if err := db.Create(&u).Error; err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case u.Name == "" && id.Name != "":
		if err := db.Model(&u).Update("name", id.Name).Error; err != nil {
			return nil, err
		}
	}
	if err := db.Create(&UserIdentity{UserID: u.ID, Provider: id.Provider, Subject: id.Subject}).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// issue creates a refresh token row and an access token for u (call inside a tx).
func (s *Service) issue(ctx context.Context, u *models.User) (*TokenPair, *RefreshToken, error) {
	access, err := s.tokens.IssueAccess(u)
	if err != nil {
		return nil, nil, err
	}
	raw, hash := newRefreshToken()
	rt := &RefreshToken{UserID: u.ID, TokenHash: hash, ExpiresAt: s.now().UTC().Add(RefreshTTL)}
	if err := dbx.Conn(ctx, s.db).Create(rt).Error; err != nil {
		return nil, nil, err
	}
	return &TokenPair{
		AccessToken:  access,
		RefreshToken: raw,
		TokenType:    "Bearer",
		ExpiresIn:    int(AccessTTL.Seconds()),
		User:         u,
	}, rt, nil
}

// Refresh rotates a refresh token. Presenting an already rotated token is treated as theft: every active
// refresh token of that user is revoked and the call fails. A token revoked by logout just fails.
func (s *Service) Refresh(ctx context.Context, token string) (*TokenPair, error) {
	now := s.now().UTC()
	var (
		pair  *TokenPair
		reuse bool
	)
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		db := dbx.Conn(ctx, s.db)
		var old RefreshToken
		if err := db.Where("token_hash = ?", hashRefreshToken(token)).Take(&old).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errBadRefresh
			}
			return err
		}
		if old.ReplacedBy != "" {
			reuse = true
			// Returning nil commits the revocation; the error is reported after the tx.
			return db.Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", old.UserID).
				Update("revoked_at", now).Error
		}
		// Logged out (or revoked by a reuse sweep): plain 401, other sessions stay alive.
		if old.RevokedAt != nil || !now.Before(old.ExpiresAt) {
			return errBadRefresh
		}
		var u models.User
		if err := db.Take(&u, "id = ?", old.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errBadRefresh
			}
			return err
		}
		p, next, err := s.issue(ctx, &u)
		if err != nil {
			return err
		}
		res := db.Model(&RefreshToken{}).Where("id = ? AND revoked_at IS NULL", old.ID).
			Updates(map[string]any{"revoked_at": now, "replaced_by": next.ID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errBadRefresh
		}
		pair = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	if reuse {
		s.log.WarnContext(ctx, "auth: refresh token reuse detected, revoked all sessions of the user")
		return nil, errBadRefresh
	}
	return pair, nil
}

// Logout revokes the given refresh token. Unknown or already revoked tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	return dbx.Conn(ctx, s.db).Model(&RefreshToken{}).
		Where("token_hash = ? AND revoked_at IS NULL", hashRefreshToken(token)).
		Update("revoked_at", s.now().UTC()).Error
}

// User returns the user with the given id; a missing (e.g. deleted) user is Unauthorized.
func (s *Service) User(ctx context.Context, id string) (*models.User, error) {
	var u models.User
	if err := dbx.Conn(ctx, s.db).Take(&u, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.Unauthorized("user not found")
		}
		return nil, err
	}
	return &u, nil
}

// retryAfter is attached as the cause of 429 errors; the controller turns it into a Retry-After header.
type retryAfter struct{ d time.Duration }

func (r retryAfter) Error() string { return "retry after " + r.d.String() }

func tooManyRequests(msg string, wait time.Duration) error {
	secs := strconv.Itoa(int(math.Ceil(wait.Seconds())))
	return apperr.TooManyRequests(msg).
		WithFields(map[string]string{"retry_after": secs}).
		Wrap(retryAfter{wait})
}
