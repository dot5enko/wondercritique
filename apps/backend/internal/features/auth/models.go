package auth

import (
	"time"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/models"
)

// UserIdentity links a user to a provider identity (table "identities"). It is named UserIdentity to keep
// Identity free for the provider result type.
type UserIdentity struct {
	models.Base
	UserID   string      `gorm:"not null;index"`
	User     models.User `gorm:"constraint:OnDelete:CASCADE"`
	Provider string      `gorm:"not null;uniqueIndex:idx_identity_provider_subject"`
	Subject  string      `gorm:"not null;uniqueIndex:idx_identity_provider_subject"`
}

func (UserIdentity) TableName() string { return "identities" }

// AuthCode is an email login code, stored as HMAC(code key, email+code).
// ConsumedAt is set when the code is used, exhausted or superseded by a newer code.
type AuthCode struct {
	models.Base
	Email      string `gorm:"not null;index"`
	CodeHash   string `gorm:"not null"`
	ExpiresAt  time.Time
	Attempts   int `gorm:"not null;default:0"`
	ConsumedAt *time.Time
}

// RefreshToken is a hashed opaque refresh token. A rotated token has RevokedAt and ReplacedBy set.
type RefreshToken struct {
	models.Base
	UserID     string      `gorm:"not null;index"`
	User       models.User `gorm:"constraint:OnDelete:CASCADE"`
	TokenHash  string      `gorm:"not null;uniqueIndex"`
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy string
}
