package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
)

// HandlerFunc is an HTTP handler that returns an error for central rendering.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts fn to http.HandlerFunc, rendering any returned error with WriteError.
func Handle(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

// JSON writes v as JSON with the given status. A nil v writes only the status (e.g. 204).
func JSON(w http.ResponseWriter, status int, v any) error {
	if v == nil {
		w.WriteHeader(status)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// ToAppError maps err to an *apperr.Error: apperr errors pass through, gorm.ErrRecordNotFound → not_found,
// context.DeadlineExceeded → 504 timeout, anything else → internal.
func ToAppError(err error) *apperr.Error {
	if e, ok := apperr.As(err); ok {
		return e
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return apperr.NotFound("not found").Wrap(err)
	case errors.Is(err, context.DeadlineExceeded):
		return apperr.New(http.StatusGatewayTimeout, "timeout", "request timed out").Wrap(err)
	}
	return apperr.Internal(err)
}

// WriteError renders err as {"error":{"code","message","fields","request_id"}}.
// 5xx errors are logged with their cause at error level; others at debug.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e := ToAppError(err)
	reqID := middleware.GetReqID(r.Context())
	level := slog.LevelDebug
	if e.Status >= 500 {
		level = slog.LevelError
	}
	slog.Log(r.Context(), level, "request error",
		"method", r.Method, "path", r.URL.Path, "request_id", reqID, "status", e.Status, "err", err)
	_ = JSON(w, e.Status, errorBody{errorPayload{Code: e.Code, Message: e.Message, Fields: e.Fields, RequestID: reqID}})
}
