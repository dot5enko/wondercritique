package app_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/app"
)

func TestValidateApp(t *testing.T) {
	if err := fx.ValidateApp(app.Module); err != nil {
		t.Fatal(err)
	}
}

// TestRouterBuilds actually constructs the root router with every controller
// registered. ValidateApp only checks the graph and does not run constructors,
// so route conflicts (chi panics on a duplicate Mount, e.g. two `/studio`
// subrouters) would otherwise only show up at boot.
func TestRouterBuilds(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	// Neutralise anything a local .env might provide.
	for _, k := range []string{"RESEND_API_KEY", "MAIL_FROM", "ADMIN_EMAILS", "JWT_SECRET", "SECRETS_KEY", "MODEL_API_KEY"} {
		t.Setenv(k, "")
	}

	var router *chi.Mux
	fxApp := fx.New(app.Module, fx.NopLogger, fx.Populate(&router))
	if err := fxApp.Err(); err != nil {
		t.Fatal(err)
	}

	// Both the studio CRUD and the agentrun action routes must resolve
	// (401 = matched a route behind auth; 404 would mean it is missing).
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/studio/catalog"},
		{http.MethodGet, "/api/v1/studio/agent"},
		{http.MethodPost, "/api/v1/studio/agents/x/preview"},
		{http.MethodPost, "/api/v1/studio/pipelines/x/validate"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}
