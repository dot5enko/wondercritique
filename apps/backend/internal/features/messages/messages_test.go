package messages_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/internal/crud"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/messages"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

const testIssuer = "messages-test"

func setup(t *testing.T) http.Handler {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	db, err := dbx.Open("file:messages_test?mode=memory&cache=shared", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.Message{}); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", testIssuer)
	messages.NewController(crud.NewRegistry(db, httpx.NewValidator(), log), mw).Routes(r)
	return r
}

// bearer issues a valid access token for userID and returns the header value.
func bearer(t *testing.T, mw *auth.Middleware, userID string) string {
	t.Helper()
	u := &models.User{Base: models.Base{ID: userID}}
	token, err := mw.IssueAccessToken(u)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + token
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestMessages exercises the endpoints as user u1; owner_id is server-set and rows are scoped.
func TestMessages(t *testing.T) {
	h := setup(t)
	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", testIssuer)
	head := bearer(t, mw, "u1")
	doAuth := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", head)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	if w := do(h, http.MethodPost, "/messages", `{"thread_id":"t1","role":"user","body":"hi"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create = %d, want 401", w.Code)
	}

	w := doAuth(http.MethodPost, "/messages", `{"thread_id":"t1","role":"user","body":"hi"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var m models.Message
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.ID == "" || m.ThreadID != "t1" || m.OwnerID != "u1" {
		t.Fatalf("created %+v", m)
	}

	w = doAuth(http.MethodPost, "/messages", `{"role":"bot","body":"`+strings.Repeat("x", 20001)+`"}`)
	if w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), `"thread_id":"is required"`) ||
		!strings.Contains(w.Body.String(), `"role":"must be one of: user, assistant, system"`) ||
		!strings.Contains(w.Body.String(), `"body":"must be at most 20000 characters long"`) {
		t.Fatalf("validation: %d %s", w.Code, w.Body)
	}

	if w := doAuth(http.MethodPost, "/messages", `{"thread_id":"t1","role":"user","body":"x","owner_id":"u"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("owner_id accepted: %d %s", w.Code, w.Body)
	}
	if w := doAuth(http.MethodPatch, "/messages/"+m.ID, `{"role":"system"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("role is not updatable: %d %s", w.Code, w.Body)
	}
	if w := doAuth(http.MethodPatch, "/messages/"+m.ID, `{"body":"edited"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"edited"`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := doAuth(http.MethodGet, "/messages?filter[thread_id]=t1&sort=role", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), m.ID) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := doAuth(http.MethodGet, "/messages?sort=body", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("sort by body allowed: %d", w.Code)
	}

	// Another user sees nothing: their list is empty and u1's row 404s for them.
	other := bearer(t, mw, "u2")
	req := httptest.NewRequest(http.MethodGet, "/messages", nil)
	req.Header.Set("Authorization", other)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK || strings.Contains(w2.Body.String(), m.ID) {
		t.Fatalf("list as other user: %d %s", w2.Code, w2.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/messages/"+m.ID, nil)
	req.Header.Set("Authorization", other)
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("get as other user = %d, want 404", w3.Code)
	}

	if w := doAuth(http.MethodDelete, "/messages/"+m.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
}
