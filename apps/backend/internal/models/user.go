package models

import (
	"strings"

	"gorm.io/gorm"
)

// User roles.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// User is an account. Login methods are attached through auth identities (one user, many providers).
type User struct {
	Base
	Email string `gorm:"uniqueIndex;not null" json:"email"` // lowercased, trimmed
	Name  string `gorm:"not null;default:''" json:"name"`
	Role  string `gorm:"not null;default:user" json:"role"`
}

// BeforeSave keeps Email normalized (trimmed, lowercased) and Role defaulted on struct saves.
func (u *User) BeforeSave(*gorm.DB) error {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	if u.Role == "" {
		u.Role = RoleUser
	}
	return nil
}
