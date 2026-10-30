package auth

import (
	"bytes"
	"context"
	"encoding/json"

	"go.uber.org/fx"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
)

// Provider is a login method ("email_code", later "google", ...), addressed as /auth/{Name()}/start|verify.
//
// Start begins the login (send a code, build an OAuth redirect, ...) and returns a JSON object for the client.
// Verify completes it and returns the authenticated external identity. Providers must only return emails they
// have verified, because the service links identities to existing users by email.
// Both receive the raw request body; use DecodeRequest to parse it. Return *apperr.Error for client errors.
type Provider interface {
	Name() string
	Start(ctx context.Context, req json.RawMessage) (StartResult, error)
	Verify(ctx context.Context, req json.RawMessage) (Identity, error)
}

// StartResult is the JSON object returned by POST /auth/{provider}/start.
type StartResult map[string]any

// Identity is an external identity proven by a provider.
type Identity struct {
	Provider string // provider name
	Subject  string // stable id within the provider
	Email    string // verified email, required
	Name     string // optional display name
}

// AsProvider registers a Provider constructor in the fx group "auth_providers".
func AsProvider(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.As(new(Provider)), fx.ResultTags(`group:"auth_providers"`)))
}

// DecodeRequest strictly decodes a provider request body into T (unknown fields rejected), calls
// Normalize() on it when *T implements it (e.g. trim/lowercase), then validates it.
func DecodeRequest[T any](raw json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, apperr.Invalid("invalid request body", nil).Wrap(err)
	}
	if n, ok := any(&v).(interface{ Normalize() }); ok {
		n.Normalize()
	}
	return v, httpx.Validate(v)
}
