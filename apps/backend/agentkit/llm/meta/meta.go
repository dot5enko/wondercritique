// Package meta implements llm.Provider for the Meta Model API (Responses API, stateless encrypted
// reasoning replay).
//
// Docs: https://dev.meta.ai/docs/protocols/responses
package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
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
	DefaultBaseURL    = "https://api.meta.ai/v1"
	DefaultModel      = ModelMuseSpark13Contributor
	DefaultMaxRetries = 3
)

// ProviderName is the value of Name() and of llm.Block.Provider on reasoning blocks produced here.
const ProviderName = "meta"

const retryBase = 500 * time.Millisecond

// Config configures a Provider.
type Config struct {
	APIKey       string
	BaseURL      string       // default DefaultBaseURL
	HTTPClient   *http.Client // default http.DefaultClient; must not set a total Timeout shorter than a stream
	DefaultModel string       // used when Request.Model is empty; default DefaultModel
	MaxRetries   int          // retries on 429/5xx/network errors before the stream starts; default 3, <0 disables
	UserAgent    string
}

// Provider streams responses from the Meta Model API. It is safe for concurrent use.
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
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	} else if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "agentkit-meta/1"
	}
	return &Provider{cfg: cfg, sleep: sleepCtx}
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return ProviderName }

// SetSleep overrides the backoff sleep; used by tests. The function must
// respect context cancellation.
func (p *Provider) SetSleep(fn func(ctx context.Context, d time.Duration) error) { p.sleep = fn }

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req *llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		body, err := buildBody(req, p.cfg.DefaultModel)
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

		err = consume(resp.Body, yield)
		if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
			err = ctxErr
		}
		if err != nil && !errors.Is(err, errStopped) {
			yield(llm.StreamEvent{}, err)
		}
	}
}

// open POSTs the request, retrying retryable failures before any response byte is consumed.
func (p *Provider) open(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := p.do(ctx, body)
		var wait time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			err = fmt.Errorf("meta: %w", err)
		case resp.StatusCode == http.StatusOK:
			return resp, nil
		default:
			apiErr := parseAPIError(resp)
			if !retryableStatus(resp.StatusCode) {
				return nil, apiErr
			}
			err, wait = apiErr, apiErr.RetryAfter
		}
		if attempt >= p.cfg.MaxRetries {
			return nil, err
		}
		if wait <= 0 {
			wait = backoff(attempt)
		}
		if serr := p.sleep(ctx, wait); serr != nil {
			return nil, serr
		}
	}
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

// retryableStatus reports whether an HTTP status may be retried. 504 is excluded: the docs say the
// same request will usually time out again.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusGatewayTimeout)
}

// backoff returns the exponential delay for attempt (0-based) with jitter in [d/2, d).
func backoff(attempt int) time.Duration {
	d := retryBase << min(attempt, 10)
	return d/2 + rand.N(d/2)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// APIError is a non-200 HTTP response from the Meta Model API.
type APIError struct {
	Status     int
	Type       string // e.g. invalid_request_error, authentication_error, rate_limit_error, server_error
	Code       string // e.g. invalid_api_key, model_not_found, rate_limit_exceeded; often empty
	Param      string
	Message    string
	RetryAfter time.Duration // from the Retry-After header, 0 if absent
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "meta: HTTP %d", e.Status)
	for _, s := range []string{e.Type, e.Code} {
		if s != "" {
			b.WriteString(" " + s)
		}
	}
	if e.Param != "" {
		fmt.Fprintf(&b, " (param %s)", e.Param)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// Temporary reports whether retrying the same request may succeed.
func (e *APIError) Temporary() bool { return retryableStatus(e.Status) }

func parseAPIError(resp *http.Response) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	var env struct {
		Error *struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    any     `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		e.Message, e.Type = env.Error.Message, env.Error.Type
		if env.Error.Param != nil {
			e.Param = *env.Error.Param
		}
		switch c := env.Error.Code.(type) {
		case string:
			e.Code = c
		case float64:
			e.Code = strconv.FormatFloat(c, 'f', -1, 64)
		}
		return e
	}
	if msg := strings.TrimSpace(string(raw)); msg != "" {
		e.Message = msg
	} else {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// StreamError is a terminal failure reported inside a 200 SSE stream (response.failed or error event).
type StreamError struct {
	Event   string // "response.failed" or "error"
	Code    string // e.g. server_error, rate_limit_exceeded, server_shutting_down
	Message string
	Param   string
}

func (e *StreamError) Error() string {
	s := "meta: " + e.Event
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}
