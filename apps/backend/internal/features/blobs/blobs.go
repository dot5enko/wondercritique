// Package blobs is a content-addressed blob store for rendered artifacts
// (screenshots and other derived files), backed by the app's SQLite.
package blobs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"

	"go.uber.org/fx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// Module registers the Blob model for migration and provides *Store.
var Module = fx.Module("blobs",
	dbx.AsModel(&Blob{}),
	fx.Provide(NewStore),
)

// ErrNotFound is returned by Get/DataURL for an unknown or oversized sha.
var ErrNotFound = errors.New("blobs: not found")

// ErrInvalidSHA is returned for a sha that is not 64 hex characters.
var ErrInvalidSHA = errors.New("blobs: invalid sha256")

// Blob is one stored object. The content hash is the primary key, so storing
// the same bytes twice is a no-op.
type Blob struct {
	SHA256    string    `json:"sha256" gorm:"primaryKey;size:64;not null"`
	MIME      string    `json:"mime" gorm:"size:255;not null;default:application/octet-stream"`
	Size      int64     `json:"size" gorm:"not null"`
	Data      []byte    `json:"-" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default pluralization.
func (Blob) TableName() string { return "blobs" }

// Store reads and writes blobs.
type Store struct{ db *gorm.DB }

// NewStore wraps the app database.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// Put stores data and returns its sha256 (hex). Re-putting identical bytes is
// idempotent. mime defaults to application/octet-stream; an empty or invalid
// mime is rejected.
func (s *Store) Put(ctx context.Context, data []byte, mimeType string) (string, error) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(mimeType); err != nil {
		return "", fmt.Errorf("blobs: invalid mime %q: %w", mimeType, err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	blob := Blob{SHA256: sha, MIME: mimeType, Size: int64(len(data)), Data: data}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&blob).Error
	if err != nil {
		return "", fmt.Errorf("blobs: put: %w", err)
	}
	return sha, nil
}

// Get returns the blob for sha, or ErrNotFound.
func (s *Store) Get(ctx context.Context, sha string) (*Blob, error) {
	if err := validateSHA(sha); err != nil {
		return nil, err
	}
	var b Blob
	err := s.db.WithContext(ctx).First(&b, "sha256 = ?", sha).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blobs: get: %w", err)
	}
	return &b, nil
}

// Exists reports whether sha is stored.
func (s *Store) Exists(ctx context.Context, sha string) (bool, error) {
	if err := validateSHA(sha); err != nil {
		return false, err
	}
	var n int64
	err := s.db.WithContext(ctx).Model(&Blob{}).Where("sha256 = ?", sha).Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("blobs: exists: %w", err)
	}
	return n > 0, nil
}

// DataURL returns the blob as a data:<mime>;base64 URL.
func (s *Store) DataURL(ctx context.Context, sha string) (string, error) {
	b, err := s.Get(ctx, sha)
	if err != nil {
		return "", err
	}
	return "data:" + b.MIME + ";base64," + base64.StdEncoding.EncodeToString(b.Data), nil
}

// URL is the application reference for a stored blob: wcblob://<sha>.
func URL(sha string) string { return "wcblob://" + sha }

// ParseURL extracts the sha from a wcblob:// URL and validates it.
func ParseURL(raw string) (string, error) {
	rest, ok := strings.CutPrefix(raw, "wcblob://")
	if !ok {
		return "", fmt.Errorf("%w: not a wcblob URL", ErrInvalidSHA)
	}
	if err := validateSHA(rest); err != nil {
		return "", err
	}
	return rest, nil
}

func validateSHA(sha string) error {
	if len(sha) != 64 {
		return fmt.Errorf("%w: want 64 hex chars, got %d", ErrInvalidSHA, len(sha))
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSHA, err)
	}
	return nil
}
