// Package responses implements llm.Provider for the OpenAI Responses API
// (stateless mode with encrypted reasoning replay). It is the general adapter
// extracted from llm/meta; the meta package is a thin preset over it.
//
// Docs: https://platform.openai.com/docs/api-reference/responses
package responses

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/internal/wire"
)

// Defaults applied by New.
const (
	DefaultBaseURL    = "https://api.meta.ai/v1"
	DefaultMaxRetries = 3
)

// DefaultName is the provider identity stamped on reasoning blocks and used in
// error messages when Config.Name is empty.
const DefaultName = "responses"

// Config configures a Provider.
type Config struct {
	APIKey       string
	BaseURL      string       // default DefaultBaseURL
	HTTPClient   *http.Client // default http.DefaultClient; must not set a total Timeout shorter than a stream
	DefaultModel string       // used when Request.Model is empty
	MaxRetries   int          // retries on 429/5xx/network errors before the stream starts; default 3, <0 disables
	UserAgent    string
	// Name is the provider identity stamped on reasoning blocks (llm.Block.Provider)
	// and returned by Name(). Reasoning blocks tagged with this name are replayed
	// verbatim on subsequent requests; blocks from other providers are dropped.
	// Defaults to DefaultName; presets (e.g. meta) set their own.
	Name string
}

// Provider streams responses from a Responses API endpoint. It is safe for concurrent use.
type Provider struct {
	cfg   Config
	sleep func(ctx context.Context, d time.Duration) error // injectable for tests
}

var _ llm.Provider = (*Provider)(nil)

// New returns a Provider with defaults applied.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	} else if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "agentkit-responses/1"
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	return &Provider{cfg: cfg, sleep: wire.SleepCtx}
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return p.cfg.Name }

// SetSleep overrides the backoff sleep; used by tests. The function must
// respect context cancellation.
func (p *Provider) SetSleep(fn func(ctx context.Context, d time.Duration) error) { p.sleep = fn }

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req *llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		body, err := buildBody(req, p.cfg.DefaultModel, p.cfg.Name)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		resp, err := p.open(ctx, body)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		defer resp.Body.Close()
		// Unblock body reads promptly when ctx is cancelled mid-stream.
		stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
		defer stop()

		err = p.consume(resp.Body, yield)
		if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
			err = ctxErr
		}
		if err != nil && !errors.Is(err, wire.ErrStop) {
			yield(llm.StreamEvent{}, err)
		}
	}
}

// open POSTs the request, retrying retryable failures before any response byte is consumed.
func (p *Provider) open(ctx context.Context, body []byte) (*http.Response, error) {
	return wire.Open(ctx, p.cfg.Name, p.cfg.MaxRetries, func() (*http.Response, error) {
		return p.do(ctx, body)
	}, p.sleep)
}

func (p *Provider) do(ctx context.Context, body []byte) (*http.Response, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	hr.Header.Set("User-Agent", p.cfg.UserAgent)
	if p.cfg.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	return p.cfg.HTTPClient.Do(hr)
}
