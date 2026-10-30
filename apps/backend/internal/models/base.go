// Package models holds GORM models shared across features.
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Base is embedded by every model: UUIDv7 string primary key, timestamps and soft delete.
type Base struct {
	ID        string         `gorm:"primaryKey;type:text" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// NewID returns a new UUIDv7 string (time ordered, so lexical order == creation order).
func NewID() string { return uuid.Must(uuid.NewV7()).String() }

// BeforeCreate assigns an ID if none is set.
func (b *Base) BeforeCreate(*gorm.DB) error {
	if b.ID == "" {
		b.ID = NewID()
	}
	return nil
}
