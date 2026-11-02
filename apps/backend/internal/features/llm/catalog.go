// models.dev catalog fetching and parsing into the in-memory provider registry.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// DefaultCatalogURL is the models.dev snapshot endpoint.
const DefaultCatalogURL = "https://models.dev/api.json"

// CatalogTTL is how old the snapshot may be before a refresh is attempted.
const CatalogTTL = 24 * time.Hour

// Adapter families (plan §3.2). `none` = listed but cannot be enabled.
const (
	AdapterResponses = "responses"
	AdapterChat      = "chat"
	AdapterAnthropic = "anthropic"
	AdapterNone      = "none"
)

// CatalogModel is one chat model of a provider as parsed from the snapshot.
type CatalogModel struct {
	ID               string
	Name             string
	Vision           bool
	Tools            bool
	ReasoningOptions []string
	ContextWindow    int
	MaxOutput        int
	Deprecated       bool
	Shape            string // per-model adapter override: responses | completions ("")
	Cost             *ModelCost
}

// ModelCost is the models.dev price in USD per million tokens (nil = unknown).
type ModelCost struct {
	Input     float64
	Output    float64
	CacheRead float64
}

// Estimate returns the USD cost of the given token counts. inputTokens is the
// uncached input; cachedTokens is billed at the cache-read price (the input
// price when the catalog lists none).
func (c ModelCost) Estimate(inputTokens, cachedTokens, outputTokens int) float64 {
	cacheRate := c.CacheRead
	if cacheRate == 0 {
		cacheRate = c.Input
	}
	return (float64(inputTokens)*c.Input + float64(cachedTokens)*cacheRate + float64(outputTokens)*c.Output) / 1e6
}

// CatalogProvider is one provider of the parsed snapshot.
type CatalogProvider struct {
	ID      string
	Name    string
	NPM     string
	Env     []string
	API     string // base URL template with ${VAR} placeholders ("" for first-party SDK providers)
	Doc     string
	Adapter string
	Models  []CatalogModel // chat models only, snapshot order
}

// catalogURLVar matches ${VAR} placeholders in api templates.
var catalogURLVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// defaultAPIBaseURLs cover first-party SDK providers that ship no `api` field.
var defaultAPIBaseURLs = map[string]string{
	"openai":    "https://api.openai.com/v1",
	"anthropic": "https://api.anthropic.com/v1",
	"meta":      "https://api.meta.ai/v1",
}

// chatProviderIDs resolve to the chat adapter regardless of npm metadata.
var chatProviderIDs = map[string]bool{
	"openrouter": true, "groq": true, "xai": true, "deepinfra": true,
	"togetherai": true, "cerebras": true, "mistral": true, "perplexity": true,
}

// ResolveAdapter maps models.dev npm/shape metadata to an agentkit adapter family.
func ResolveAdapter(npm, shape, providerID string) string {
	switch shape {
	case "responses":
		return AdapterResponses
	case "completions":
		return AdapterChat
	}
	switch npm {
	case "@ai-sdk/openai":
		return AdapterResponses
	case "@ai-sdk/openai-compatible":
		return AdapterChat
	case "@ai-sdk/anthropic":
		return AdapterAnthropic
	}
	if chatProviderIDs[providerID] {
		return AdapterChat
	}
	return AdapterNone
}

/* ---------- raw models.dev shapes ---------- */

type rawReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type rawProviderRef struct {
	NPM   string `json:"npm"`
	API   string `json:"api"`
	Shape string `json:"shape"`
}

type rawModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	ToolCall   bool   `json:"tool_call"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost *struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		CacheRead float64 `json:"cache_read"`
	} `json:"cost"`
	ReasoningOptions []rawReasoningOption `json:"reasoning_options"`
	Provider         *rawProviderRef      `json:"provider"`
}

type rawProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Env    []string            `json:"env"`
	NPM    string              `json:"npm"`
	API    string              `json:"api"`
	Doc    string              `json:"doc"`
	Models map[string]rawModel `json:"models"`
}

// parseCatalog parses raw api.json into providers (chat models only), keeping
// the snapshot's provider order (the top-level JSON object is read with a
// streaming decoder so insertion order is preserved).
func parseCatalog(body []byte) ([]*CatalogProvider, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("llm: parse models.dev snapshot: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("llm: parse models.dev snapshot: not a JSON object")
	}
	var out []*CatalogProvider
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("llm: parse models.dev snapshot: %w", err)
		}
		id, _ := keyTok.(string)
		var p rawProvider
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("llm: parse models.dev snapshot: provider %s: %w", id, err)
		}
		out = append(out, buildProvider(id, p))
	}
	return out, nil
}

func buildProvider(id string, p rawProvider) *CatalogProvider {
	cp := &CatalogProvider{
		ID:      id,
		Name:    p.Name,
		NPM:     p.NPM,
		Env:     p.Env,
		API:     p.API,
		Doc:     p.Doc,
		Adapter: ResolveAdapter(p.NPM, "", id),
	}
	for _, m := range p.Models {
		if m.Type != "" && m.Type != "chat" {
			continue
		}
		// A per-model shape override changes the wire protocol; when it
		// differs from the provider's adapter the model cannot be driven
		// correctly by the single provider-level adapter and is skipped.
		shape := ""
		if m.Provider != nil {
			shape = m.Provider.Shape
		}
		if shape != "" && ResolveAdapter(p.NPM, shape, id) != cp.Adapter {
			continue
		}
		cm := CatalogModel{
			ID:            m.ID,
			Name:          m.Name,
			Vision:        contains(m.Modalities.Input, "image"),
			Tools:         m.ToolCall,
			ContextWindow: m.Limit.Context,
			MaxOutput:     m.Limit.Output,
			Deprecated:    m.Status == "deprecated",
			Shape:         shape,
		}
		if m.Cost != nil {
			cm.Cost = &ModelCost{Input: m.Cost.Input, Output: m.Cost.Output, CacheRead: m.Cost.CacheRead}
		}
		for _, ro := range m.ReasoningOptions {
			if ro.Type == "effort" && len(ro.Values) > 0 {
				cm.ReasoningOptions = ro.Values
			}
		}
		cp.Models = append(cp.Models, cm)
	}
	// Deterministic order within a provider (the snapshot's nested JSON
	// object order is not preserved by map decoding).
	sort.Slice(cp.Models, func(i, j int) bool { return cp.Models[i].ID < cp.Models[j].ID })
	return cp
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// BaseURLTemplate returns the effective base URL template of a provider:
// the `api` field when present, else the known first-party default.
func (p *CatalogProvider) BaseURLTemplate() (string, error) {
	if p.API != "" {
		return p.API, nil
	}
	if d, ok := defaultAPIBaseURLs[p.ID]; ok {
		return d, nil
	}
	return "", apperr.Unavailable(fmt.Sprintf("provider %q has no base URL", p.ID))
}

// BaseURL substitutes ${VAR} placeholders with vars. Every placeholder must be
// provided; missing ones surface as unavailable errors.
func BaseURL(template string, vars map[string]string) (string, error) {
	var missing []string
	out := catalogURLVar.ReplaceAllStringFunc(template, func(m string) string {
		name := catalogURLVar.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok && v != "" {
			return v
		}
		missing = append(missing, name)
		return m
	})
	if len(missing) > 0 {
		return "", apperr.Unavailable("missing base URL variable(s): " + strings.Join(missing, ", "))
	}
	return out, nil
}

/* ---------- fetching ---------- */

// fetchCatalog GETs the models.dev snapshot. It returns (body, etag, notModified, err).
func (s *Service) fetchCatalog(ctx context.Context, etag string) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.catURL, nil)
	if err != nil {
		return nil, "", false, fmt.Errorf("llm: catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "wondercritique-backend/1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("llm: fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", false, fmt.Errorf("llm: fetch catalog: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", false, fmt.Errorf("llm: read catalog: %w", err)
	}
	return body, resp.Header.Get("ETag"), false, nil
}
