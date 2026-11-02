package auth

import (
	"log/slog"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

func TestBootstrapAdmins(t *testing.T) {
	db, err := dbx.Open("file:boot_"+models.NewID()+"?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	existing := models.User{Email: "old@example.com"}
	gone := models.User{Email: "gone@example.com"}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&gone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&gone).Error; err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AdminEmails: []string{" New@Example.com ", "old@example.com", "gone@example.com", ""}}
	log := slog.New(slog.DiscardHandler)
	for range 2 { // idempotent
		if err := bootstrapAdmins(cfg, db, log); err != nil {
			t.Fatal(err)
		}
	}

	role := func(email string) string {
		var u models.User
		if err := db.Unscoped().Where("email = ?", email).Take(&u).Error; err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		return u.Role
	}
	if r := role("new@example.com"); r != models.RoleAdmin {
		t.Errorf("created user role = %q", r)
	}
	if r := role("old@example.com"); r != models.RoleAdmin {
		t.Errorf("promoted user role = %q", r)
	}
	if r := role("gone@example.com"); r != models.RoleUser {
		t.Errorf("disabled user role = %q, want untouched", r)
	}
	var n int64
	db.Unscoped().Model(&models.User{}).Count(&n)
	if n != 3 {
		t.Errorf("users = %d, want 3", n)
	}
}
