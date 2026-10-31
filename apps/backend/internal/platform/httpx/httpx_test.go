package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

type address struct {
	City string `json:"city" validate:"required"`
}

type createReq struct {
	Title   string   `json:"title" validate:"required,max=5"`
	Email   string   `json:"email" validate:"omitempty,email"`
	Count   int      `json:"count" validate:"gte=0"`
	Address *address `json:"address"`
}

func decode(body string) (createReq, error) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	return httpx.Decode[createReq](r)
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		fields map[string]string
	}{
		{"ok", `{"title":"hi","count":1}`, 0, nil},
		{"empty", ``, 400, nil},
		{"malformed", `{"title":`, 400, nil},
		{"syntax", `{"title" 1}`, 400, nil},
		{"unknown field", `{"title":"hi","bogus":1}`, 400, map[string]string{"bogus": "unknown field"}},
		{"wrong type", `{"title":"hi","count":"x"}`, 400, map[string]string{"count": "must be an integer"}},
		{"trailing value", `{"title":"hi"}{}`, 400, nil},
		{"validation", `{"title":"toolong","email":"nope","count":-1,"address":{}}`, 400, map[string]string{
			"title":        "must be at most 5 characters long",
			"email":        "must be a valid email address",
			"count":        "must be greater than or equal to 0",
			"address.city": "is required",
		}},
		{"too large", `{"title":"` + strings.Repeat("a", httpx.MaxBodyBytes) + `"}`, 413, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := decode(tt.body)
			if tt.status == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if v.Title != "hi" || v.Count != 1 {
					t.Fatalf("decoded %+v", v)
				}
				return
			}
			e, ok := apperr.As(err)
			if !ok {
				t.Fatalf("want *apperr.Error, got %v", err)
			}
			if e.Status != tt.status {
				t.Fatalf("status = %d, want %d (%v)", e.Status, tt.status, err)
			}
			for k, want := range tt.fields {
				if got := e.Fields[k]; got != want {
					t.Errorf("fields[%q] = %q, want %q (all: %v)", k, got, want, e.Fields)
				}
			}
			if tt.fields != nil && len(e.Fields) != len(tt.fields) {
				t.Errorf("fields = %v, want %v", e.Fields, tt.fields)
			}
		})
	}
}

type errResp struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

func TestWriteError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"not found", apperr.NotFound("message not found"), 404, "not_found", "message not found"},
		{"wrapped", errors.Join(errors.New("ctx"), apperr.Conflict("taken")), 409, "conflict", "taken"},
		{"invalid", apperr.Invalid("bad", map[string]string{"title": "is required"}), 400, "invalid", "bad"},
		{"gorm not found", gorm.ErrRecordNotFound, 404, "not_found", "not found"},
		{"unknown hidden", errors.New("secret db detail"), 500, "internal", "internal error"},
		{"internal hides cause", apperr.Internal(errors.New("secret")), 500, "internal", "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			httpx.WriteError(w, httptest.NewRequest(http.MethodGet, "/", nil), tt.err)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type = %q", ct)
			}
			var body errResp
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tt.code || body.Error.Message != tt.message {
				t.Fatalf("body = %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("leaked cause: %s", w.Body.String())
			}
		})
	}
}

func TestHandle(t *testing.T) {
	h := httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Has("fail") {
			return apperr.Forbidden("nope")
		}
		return httpx.JSON(w, http.StatusCreated, map[string]string{"ok": "yes"})
	})

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"ok":"yes"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/?fail", nil))
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
