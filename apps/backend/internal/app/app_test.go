package app_test

import (
	"testing"

	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/app"
)

func TestValidateApp(t *testing.T) {
	if err := fx.ValidateApp(app.Module); err != nil {
		t.Fatal(err)
	}
}
