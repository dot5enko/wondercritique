package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/config"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/models"
)

const (
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 30 * 24 * time.Hour
	jwtLeeway  = 30 * time.Second
)

// Clock returns the current time; replaced in tests.
type Clock func() time.Time

// keys holds per-purpose keys derived from JWT_SECRET, so the raw secret is never used for two things.
type keys struct {
	jwt  []byte
	code []byte
}

// newKeys derives keys from cfg.JWTSecret. In dev an empty secret is replaced by a random one
// (tokens then do not survive restarts); config.Validate already enforces ≥32 bytes in prod.
func newKeys(cfg *config.Config, log *slog.Logger) (*keys, error) {
	secret := []byte(cfg.JWTSecret)
	if len(secret) == 0 {
		if !cfg.IsDev() {
			return nil, errors.New("auth: JWT_SECRET is required outside dev")
		}
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
		log.Warn("auth: JWT_SECRET is empty, using a random ephemeral secret; tokens will not survive restarts")
	}
	return &keys{
		jwt:  derive(secret, "wonder-critique/auth/jwt"),
		code: derive(secret, "wonder-critique/auth/email-code"),
	}, nil
}

func derive(secret []byte, label string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// Claims are the access token claims: sub (user id), role, iss, iat, exp, jti.
type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// UserID returns the subject.
func (c *Claims) UserID() string { return c.Subject }

// Tokens issues and verifies access tokens and mints refresh tokens.
type Tokens struct {
	key    []byte
	issuer string
	now    Clock
	parser *jwt.Parser
}

func newTokens(k *keys, cfg *config.Config, now Clock) *Tokens {
	return &Tokens{
		key:    k.jwt,
		issuer: cfg.JWTIssuer,
		now:    now,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(cfg.JWTIssuer),
			jwt.WithLeeway(jwtLeeway),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithTimeFunc(now),
		),
	}
}

// IssueAccess signs an HS256 access token for u valid for AccessTTL.
func (t *Tokens) IssueAccess(u *models.User) (string, error) {
	now := t.now()
	c := Claims{
		Role: u.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			Issuer:    t.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTTL)),
			ID:        models.NewID(),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.key)
	if err != nil {
		return "", fmt.Errorf("auth: sign access token: %w", err)
	}
	return s, nil
}

// ParseAccess verifies an access token (HS256 only, issuer, exp/iat with 30s leeway, non-empty sub).
func (t *Tokens) ParseAccess(raw string) (*Claims, error) {
	var c Claims
	_, err := t.parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) { return t.key, nil })
	if err != nil {
		return nil, err
	}
	if c.Subject == "" {
		return nil, errors.New("auth: token has no subject")
	}
	return &c, nil
}

// newRefreshToken returns an opaque token (32 random bytes, base64url) and the hash to store.
func newRefreshToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashRefreshToken(token)
}

func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
