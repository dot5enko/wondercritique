// Package anthropic implements llm.Provider for the Anthropic Messages API
// (SSE streaming, extended thinking with signature replay).
//
// Docs: https://docs.anthropic.com/en/api/messages
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/internal/wire"
)

// Defaults applied by New.
const (
	DefaultBaseURL    = "https://api.anthropic.com/v1"
	DefaultMaxRetries = 3
	// DefaultMaxTokens is used when the request carries no MaxOutputTokens —
	// the Messages API requires max_tokens on every request.
	DefaultMaxTokens = 8192
	// Version is the anthropic-version header sent with every request.
	Version = "2023-06-01"
)

// DefaultName is the provider identity used in error messages and on reasoning
// blocks when Config.Name is empty.
const DefaultName = "anthropic"

// Config configures a Provider.
type Config struct {
	APIKey       string
	BaseURL      string       // default DefaultBaseURL
	HTTPClient   *http.Client // default http.DefaultClient; must not set a total Timeout shorter than a stream
	DefaultModel string       // used when Request.Model is empty
	MaxRetries   int          // retries on 429/5xx/network errors before the stream starts; default 3, <0 disables
	UserAgent    string
	// Name is the provider identity stamped on reasoning blocks (llm.Block.Provider)
	// and returned by Name(). Defaults to DefaultName.
	Name string
}

// Provider streams Messages API responses. It is safe for concurrent use.
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
		cfg.UserAgent = "agentkit-anthropic/1"
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
		resp, err := wire.Open(ctx, p.cfg.Name, p.cfg.MaxRetries, func() (*http.Response, error) {
			return p.do(ctx, body)
		}, p.sleep)
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

func (p *Provider) do(ctx context.Context, body []byte) (*http.Response, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "text/event-stream")
	hr.Header.Set("anthropic-version", Version)
	hr.Header.Set("User-Agent", p.cfg.UserAgent)
	if p.cfg.APIKey != "" {
		hr.Header.Set("x-api-key", p.cfg.APIKey)
	}
	return p.cfg.HTTPClient.Do(hr)
}

// thinkingOpaque is the Opaque payload stored on reasoning blocks: the thinking
// text plus its signature, both replayed verbatim in the assistant content.
type thinkingOpaque struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// effortBudget maps a reasoning effort level to an extended-thinking token budget.
var effortBudget = map[string]int{
	"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384, "xhigh": 32768,
}

// buildBody maps an llm.Request to the Messages API request body.
func buildBody(req *llm.Request, defaultModel, name string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	messages, err := buildMessages(req.Messages, name)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":    model,
		"stream":   true,
		"messages": messages,
	}
	maxTokens := req.MaxOutputTokens
	var budget int
	if req.ReasoningEffort != "" {
		budget, err = thinkingBudget(req.ReasoningEffort)
		if err != nil {
			return nil, err
		}
	}
	if budget > 0 {
		// The budget must be smaller than max_tokens.
		if maxTokens <= budget {
			maxTokens = budget + 1024
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	body["max_tokens"] = maxTokens
	if req.System != "" {
		body["system"] = req.System
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			schema := json.RawMessage(t.Schema)
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools[i] = map[string]any{
				"name": t.Name, "description": t.Description, "input_schema": schema,
			}
		}
		body["tools"] = tools
	}
	if req.SafetyID != "" {
		body["metadata"] = map[string]any{"user_id": req.SafetyID}
	}
	for k, v := range req.Extra {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	return json.Marshal(body)
}

func thinkingBudget(effort string) (int, error) {
	b, ok := effortBudget[strings.ToLower(effort)]
	if !ok {
		return 0, fmt.Errorf("%s: invalid reasoning effort %q", DefaultName, effort)
	}
	return b, nil
}

// content maps a message block to one Messages API content object.
func content(b llm.Block, name string) (map[string]any, error) {
	switch b.Type {
	case llm.BlockText:
		return map[string]any{"type": "text", "text": b.Text}, nil
	case llm.BlockImage:
		src, err := imageSource(b.ImageURL)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "image", "source": src}, nil
	case llm.BlockReasoning:
		if b.Provider != "" && b.Provider != name {
			return nil, nil // reasoning from another provider is not replayable here
		}
		var o thinkingOpaque
		if len(b.Opaque) == 0 || json.Unmarshal(b.Opaque, &o) != nil || o.Thinking == "" {
			return nil, nil // nothing replayable
		}
		out := map[string]any{"type": "thinking", "thinking": o.Thinking}
		if o.Signature != "" {
			out["signature"] = o.Signature
		}
		return out, nil
	case llm.BlockToolCall:
		args := b.Input
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		return map[string]any{
			"type": "tool_use", "id": b.ToolCallID, "name": b.ToolName, "input": args,
		}, nil
	}
	return nil, nil
}

// imageSource converts a data: URL or https URL to an Anthropic image source.
func imageSource(url string) (map[string]any, error) {
	if mt, data, ok := strings.Cut(url, ","); ok && strings.HasPrefix(mt, "data:") {
		media := strings.TrimPrefix(mt, "data:")
		enc := ""
		if i := strings.IndexByte(media, ';'); i >= 0 {
			enc = media[i+1:]
			media = media[:i]
		}
		if media == "" {
			media = "image/png"
		}
		if enc != "base64" {
			plain, uerr := neturl.PathUnescape(data)
			if uerr != nil {
				return nil, fmt.Errorf("%s: bad data URL: %w", DefaultName, uerr)
			}
			data = base64.StdEncoding.EncodeToString([]byte(plain))
		}
		return map[string]any{"type": "base64", "media_type": media, "data": data}, nil
	}
	if u, err := neturl.Parse(url); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		return map[string]any{"type": "url", "url": url}, nil
	}
	return nil, fmt.Errorf("%s: unsupported image URL %q", DefaultName, url)
}

// buildMessages maps conversation history to Messages API messages. System
// content arrives as the top-level system field (handled by buildBody).
func buildMessages(messages []llm.Message, name string) ([]map[string]any, error) {
	var out []map[string]any
	for _, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			var contentArr []map[string]any
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockText:
					contentArr = append(contentArr, map[string]any{"type": "text", "text": b.Text})
				case llm.BlockImage:
					src, err := imageSource(b.ImageURL)
					if err != nil {
						return nil, err
					}
					contentArr = append(contentArr, map[string]any{"type": "image", "source": src})
				}
			}
			if len(contentArr) == 0 {
				contentArr = append(contentArr, map[string]any{"type": "text", "text": m.Text()})
			}
			out = append(out, map[string]any{"role": "user", "content": contentArr})
		case llm.RoleAssistant:
			var contentArr []map[string]any
			for _, b := range m.Blocks {
				c, err := content(b, name)
				if err != nil {
					return nil, err
				}
				if c != nil {
					contentArr = append(contentArr, c)
				}
			}
			if len(contentArr) == 0 {
				contentArr = append(contentArr, map[string]any{"type": "text", "text": ""})
			}
			out = append(out, map[string]any{"role": "assistant", "content": contentArr})
		case llm.RoleTool:
			// Tool results ride in a user message.
			var contentArr []map[string]any
			for _, b := range m.Blocks {
				if b.Type != llm.BlockToolResult {
					continue
				}
				contentArr = append(contentArr, map[string]any{
					"type": "tool_result", "tool_use_id": b.ToolCallID,
					"content": b.Output, "is_error": b.IsError,
				})
			}
			if len(contentArr) > 0 {
				out = append(out, map[string]any{"role": "user", "content": contentArr})
			}
		}
	}
	return out, nil
}
