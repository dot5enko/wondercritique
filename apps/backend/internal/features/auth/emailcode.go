package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/auth/mailer"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
)

const (
	ProviderEmailCode = "email_code"

	CodeTTL         = 10 * time.Minute
	CodeMaxAttempts = 5
	CodeCooldown    = 60 * time.Second
)

// errBadCode is deliberately the same for unknown email, wrong, expired, exhausted or superseded codes.
var errBadCode = apperr.Unauthorized("invalid or expired code")

// EmailCodeProvider logs users in with a 6-digit code sent by email.
type EmailCodeProvider struct {
	db     *gorm.DB
	mailer mailer.Mailer
	key    []byte
	now    Clock
	log    *slog.Logger
}

func newEmailCodeProvider(db *gorm.DB, m mailer.Mailer, k *keys, now Clock, log *slog.Logger) *EmailCodeProvider {
	return &EmailCodeProvider{db: db, mailer: m, key: k.code, now: now, log: log}
}

func (p *EmailCodeProvider) Name() string { return ProviderEmailCode }

type emailCodeStartReq struct {
	Email string `json:"email" validate:"required,email,max=254"`
}

func (r *emailCodeStartReq) Normalize() { r.Email = NormalizeEmail(r.Email) }

type emailCodeVerifyReq struct {
	Email string `json:"email" validate:"required,email,max=254"`
	Code  string `json:"code" validate:"required,len=6,numeric"`
}

func (r *emailCodeVerifyReq) Normalize() {
	r.Email = NormalizeEmail(r.Email)
	r.Code = strings.TrimSpace(r.Code)
}

// Start invalidates previous codes and emails a new one, at most once per CodeCooldown per email.
// The response is the same whether or not an account exists for the email.
func (p *EmailCodeProvider) Start(ctx context.Context, raw json.RawMessage) (StartResult, error) {
	req, err := DecodeRequest[emailCodeStartReq](raw)
	if err != nil {
		return nil, err
	}
	email := req.Email
	code := newCode()
	now := p.now().UTC()
	row := AuthCode{Email: email, CodeHash: p.hash(email, code), ExpiresAt: now.Add(CodeTTL)}

	err = dbx.Tx(ctx, p.db, func(ctx context.Context) error {
		var last AuthCode
		err := dbx.Conn(ctx, p.db).Unscoped().Where("email = ?", email).Order("created_at DESC").Limit(1).Take(&last).Error
		switch {
		case err == nil:
			if wait := last.CreatedAt.Add(CodeCooldown).Sub(now); wait > 0 {
				return tooManyRequests("please wait before requesting a new code", wait)
			}
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		if err := dbx.Conn(ctx, p.db).Model(&AuthCode{}).
			Where("email = ? AND consumed_at IS NULL", email).Update("consumed_at", now).Error; err != nil {
			return err
		}
		row.CreatedAt = now
		return dbx.Conn(ctx, p.db).Create(&row).Error
	})
	if err != nil {
		return nil, err
	}

	// Sent outside the tx so a slow mail API does not hold the SQLite write lock. On failure the code is
	// removed so the cooldown does not block a retry.
	if err := p.mailer.Send(ctx, mailer.Email{
		To:      email,
		Subject: "Your login code: " + code,
		Text:    fmt.Sprintf("Your login code is %s. It expires in %d minutes.\n\nIf you did not request it, ignore this email.", code, int(CodeTTL.Minutes())),
		HTML:    fmt.Sprintf("<p>Your login code is <strong>%s</strong>. It expires in %d minutes.</p><p>If you did not request it, ignore this email.</p>", code, int(CodeTTL.Minutes())),
	}); err != nil {
		if derr := p.db.WithContext(context.WithoutCancel(ctx)).Unscoped().Delete(&row).Error; derr != nil {
			p.log.ErrorContext(ctx, "auth: delete unsent code", "err", derr)
		}
		return nil, apperr.Unavailable("could not send the code, try again later").Wrap(err)
	}
	return StartResult{"sent": true, "expires_in": int(CodeTTL.Seconds())}, nil
}

// Verify checks the latest live code for the email. Every check counts as an attempt; after
// CodeMaxAttempts the code is dead and a new one must be requested.
func (p *EmailCodeProvider) Verify(ctx context.Context, raw json.RawMessage) (Identity, error) {
	req, err := DecodeRequest[emailCodeVerifyReq](raw)
	if err != nil {
		return Identity{}, err
	}
	email := req.Email
	now := p.now().UTC()
	db := dbx.Conn(ctx, p.db)

	var c AuthCode
	err = db.Where("email = ? AND consumed_at IS NULL AND expires_at > ?", email, now).
		Order("created_at DESC").Limit(1).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Identity{}, errBadCode
	}
	if err != nil {
		return Identity{}, err
	}

	// Count the attempt atomically before comparing, so parallel guesses cannot exceed the limit.
	res := db.Model(&AuthCode{}).
		Where("id = ? AND consumed_at IS NULL AND attempts < ?", c.ID, CodeMaxAttempts).
		Update("attempts", gorm.Expr("attempts + 1"))
	if res.Error != nil {
		return Identity{}, res.Error
	}
	if res.RowsAffected == 0 {
		return Identity{}, errBadCode
	}

	stored, err := hex.DecodeString(c.CodeHash)
	if err != nil {
		return Identity{}, apperr.Internal(fmt.Errorf("auth: corrupt code hash %s: %w", c.ID, err))
	}
	if !hmac.Equal(stored, p.mac(email, req.Code)) {
		return Identity{}, errBadCode
	}

	res = db.Model(&AuthCode{}).Where("id = ? AND consumed_at IS NULL", c.ID).Update("consumed_at", now)
	if res.Error != nil {
		return Identity{}, res.Error
	}
	if res.RowsAffected == 0 { // consumed concurrently
		return Identity{}, errBadCode
	}
	return Identity{Provider: ProviderEmailCode, Subject: email, Email: email}, nil
}

func (p *EmailCodeProvider) mac(email, code string) []byte {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(email))
	m.Write([]byte{0})
	m.Write([]byte(code))
	return m.Sum(nil)
}

func (p *EmailCodeProvider) hash(email, code string) string {
	return hex.EncodeToString(p.mac(email, code))
}

// newCode returns a uniformly random 6-digit, zero-padded code.
func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// NormalizeEmail trims and lowercases an email address.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
