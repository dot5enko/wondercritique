// Package meta implements llm.Provider for the Meta Model API: a thin preset
// over the generic Responses API adapter (llm/responses) with Meta's base URL,
// default model and provider identity.
//
// Docs: https://dev.meta.ai/docs/protocols/responses
package meta

import (
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/responses"
)

// Models served by the Responses API. Contributor variants let Meta train on request content.
const (
	ModelMuseSpark13Contributor = "muse-spark-1.3-contributor"
	ModelMuseSpark12Contributor = "muse-spark-1.2-contributor"
	ModelMuseSpark13            = "muse-spark-1.3"
	ModelMuseSpark12            = "muse-spark-1.2"
	ModelMuseSpark11            = "muse-spark-1.1"
	ModelMuseWonder10           = "muse-wonder-1.0"
)

// Defaults applied by New.
const (
	DefaultBaseURL = "https://api.meta.ai/v1"
	DefaultModel   = ModelMuseSpark13Contributor
)

// ProviderName is the value of Name() and of llm.Block.Provider on reasoning blocks produced here.
const ProviderName = "meta"

// Config configures a Provider. It is the generic responses.Config (same fields).
type Config = responses.Config

// APIError is a non-200 HTTP response from the Meta Model API.
type APIError = responses.APIError

// StreamError is a terminal failure reported inside a 200 SSE stream.
type StreamError = responses.StreamError

// Provider streams responses from the Meta Model API. It is safe for concurrent use.
type Provider struct {
	*responses.Provider
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider with Meta defaults applied.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "agentkit-meta/1"
	}
	cfg.Name = ProviderName
	return &Provider{Provider: responses.New(cfg)}
}

// BuildInput maps conversation history to Responses API input items, replaying
// reasoning blocks tagged with this provider's identity. Exported for hosts
// that need to preview what a request will contain.
func BuildInput(messages []llm.Message) ([]map[string]any, error) {
	return responses.BuildInputAs(ProviderName, messages)
}
