package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// bootstrapAdmins upserts every ADMIN_EMAILS address as a user with role admin: missing users are created,
// existing ones promoted. Soft-deleted accounts are left alone (logged). Login still goes through the normal
// providers; identities link to these users by email. A promoted user's role reaches the access token on the
// next refresh or login.
func bootstrapAdmins(cfg *config.Config, db *gorm.DB, log *slog.Logger) error {
	return dbx.Tx(context.Background(), db, func(ctx context.Context) error {
		conn := dbx.Conn(ctx, db)
		for _, raw := range cfg.AdminEmails {
			email := strings.ToLower(strings.TrimSpace(raw))
			if email == "" {
				continue
			}
			var u models.User
			err := conn.Unscoped().Where("email = ?", email).Take(&u).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				if err := conn.Create(&models.User{Email: email, Role: models.RoleAdmin}).Error; err != nil {
					return fmt.Errorf("auth: create admin %s: %w", email, err)
				}
				log.Info("admin user created", "email", email)
			case err != nil:
				return fmt.Errorf("auth: look up admin %s: %w", email, err)
			case u.DeletedAt.Valid:
				log.Warn("ADMIN_EMAILS entry is a disabled account; skipped", "email", email)
			case u.Role != models.RoleAdmin:
				if err := conn.Model(&u).Update("role", models.RoleAdmin).Error; err != nil {
					return fmt.Errorf("auth: promote admin %s: %w", email, err)
				}
				log.Info("user promoted to admin", "email", email)
			}
		}
		return nil
	})
}
