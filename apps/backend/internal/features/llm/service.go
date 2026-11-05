// The LLM provider Service: catalog sync/persistence, enabled-provider state,
// key management and provider construction for the runtime and catalog.
package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// ModelInfo is one model offered by an enabled provider (catalog + runtime).
type ModelInfo struct {
	ProviderID       string
	ModelID          string
	DisplayName      string
	Vision           bool
	Tools            bool
	ReasoningOptions []string
	ContextWindow    int
	MaxOutput        int
	Deprecated       bool
}

// Service manages the models.dev catalog and enabled providers. Safe for concurrent use.
type Service struct {
	db     *gorm.DB
	cfg    *config.Config
	log    *slog.Logger
	http   *http.Client
	catURL string

	aesKey []byte

	mu        sync.RWMutex
	providers map[string]*CatalogProvider // parsed snapshot, keyed by provider id
	order     []string                    // snapshot order
	syncedAt  time.Time
}

// NewService builds the provider Service.
func NewService(db *gorm.DB, cfg *config.Config, log *slog.Logger) (*Service, error) {
	key, err := secretsKey(cfg.SecretsKey, cfg.JWTSecret, cfg.IsDev())
	if err != nil {
		return nil, err
	}
	return &Service{
		db:     db,
		cfg:    cfg,
		log:    log,
		http:   &http.Client{Timeout: 60 * time.Second},
		catURL: DefaultCatalogURL,
		aesKey: key,
	}, nil
}

// SyncedAt returns when the models.dev snapshot was last fetched (zero when never).
func (s *Service) SyncedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncedAt
}

/* ---------- sync ---------- */

// loadSnapshot returns the persisted snapshot row (nil when absent).
func (s *Service) loadSnapshot(ctx context.Context) (*models.LLMCatalog, error) {
	var row models.LLMCatalog
	err := dbx.Conn(ctx, s.db).First(&row, "id = ?", models.LLMCatalogRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("llm: load catalog snapshot: %w", err)
	}
	return &row, nil
}

// Sync refreshes the snapshot when missing or older than CatalogTTL and
// (re)parses the in-memory registry. Call at startup and periodically.
func (s *Service) Sync(ctx context.Context) error {
	row, err := s.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	if row != nil && time.Since(row.FetchedAt) < CatalogTTL {
		return s.reparse(row.FetchedAt, row.Body)
	}
	return s.Refresh(ctx)
}

// Refresh force-fetches the snapshot, persists it and reparses the registry.
func (s *Service) Refresh(ctx context.Context) error {
	row, err := s.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	var etag string
	if row != nil {
		etag = row.ETag
	}
	body, newETag, notModified, err := s.fetchCatalog(ctx, etag)
	if err != nil {
		return err
	}
	if notModified {
		// Body unchanged; the sync itself is still fresh.
		now := time.Now()
		if err := dbx.Conn(ctx, s.db).Model(&models.LLMCatalog{}).
			Where("id = ?", models.LLMCatalogRowID).
			Updates(map[string]any{"fetched_at": now}).Error; err != nil {
			return fmt.Errorf("llm: touch catalog snapshot: %w", err)
		}
		return s.reparse(now, row.Body)
	}
	now := time.Now()
	saved := models.LLMCatalog{ID: models.LLMCatalogRowID, FetchedAt: now, ETag: newETag, Body: string(body)}
	if err := dbx.Conn(ctx, s.db).Save(&saved).Error; err != nil {
		return fmt.Errorf("llm: save catalog snapshot: %w", err)
	}
	return s.reparse(now, string(body))
}

// reparse rebuilds the in-memory registry from a snapshot body.
// Providers outside AllowedProviders and models outside the Meta family
// (IsAllowedModel) are dropped, so only openrouter + meta with Meta models
// are ever offered (setup wizard scope, 2026-11-05).
func (s *Service) reparse(fetchedAt time.Time, body string) error {
	providers, err := parseCatalog([]byte(body))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers = make(map[string]*CatalogProvider, len(providers))
	s.order = s.order[:0]
	for _, p := range providers {
		if !IsAllowedProvider(p.ID) {
			continue
		}
		kept := p.Models[:0]
		for _, m := range p.Models {
			if IsAllowedModel(p.ID, m.ID) {
				kept = append(kept, m)
			}
		}
		p.Models = kept
		s.providers[p.ID] = p
		s.order = append(s.order, p.ID)
	}
	s.syncedAt = fetchedAt
	return nil
}

// Catalog returns the parsed providers in snapshot order (nil map entries when
// never synced).
func (s *Service) Catalog() ([]*CatalogProvider, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*CatalogProvider, 0, len(s.order))
	for _, id := range s.order {
		if p, ok := s.providers[id]; ok {
			out = append(out, p)
		}
	}
	return out, s.syncedAt
}

// CatalogEntry returns one parsed provider.
func (s *Service) CatalogEntry(providerID string) (*CatalogProvider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.providers[providerID]
	return p, ok
}

// ModelCost returns the models.dev price of providerID/modelID (nil when the
// catalog does not list one).
func (s *Service) ModelCost(providerID, modelID string) *ModelCost {
	p, ok := s.CatalogEntry(providerID)
	if !ok {
		return nil
	}
	for i := range p.Models {
		if p.Models[i].ID == modelID {
			return p.Models[i].Cost
		}
	}
	return nil
}

/* ---------- provider state ---------- */

// stateRow returns the stored state row for a provider (nil when absent).
func (s *Service) stateRow(ctx context.Context, providerID string) (*models.LLMProvider, error) {
	var row models.LLMProvider
	err := dbx.Conn(ctx, s.db).First(&row, "provider_id = ?", providerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("llm: load provider state: %w", err)
	}
	return &row, nil
}

// enabled reports whether the provider row exists and is enabled.
func (s *Service) enabled(ctx context.Context, providerID string) (bool, error) {
	row, err := s.stateRow(ctx, providerID)
	if err != nil || row == nil {
		return false, err
	}
	return row.Enabled, nil
}

// EnabledModels returns the models of every enabled provider, in snapshot order.
func (s *Service) EnabledModels() []ModelInfo {
	// Provider enablement is read without a request context (catalog endpoint
	// context is fine, but callers may hold none); use a detached context.
	ctx := context.WithoutCancel(context.Background())
	var out []ModelInfo
	provs, _ := s.Catalog()
	for _, p := range provs {
		on, err := s.enabled(ctx, p.ID)
		if err != nil || !on {
			continue
		}
		for _, m := range p.Models {
			out = append(out, ModelInfo{
				ProviderID:       p.ID,
				ModelID:          m.ID,
				DisplayName:      m.Name,
				Vision:           m.Vision,
				Tools:            m.Tools,
				ReasoningOptions: m.ReasoningOptions,
				ContextWindow:    m.ContextWindow,
				MaxOutput:        m.MaxOutput,
				Deprecated:       m.Deprecated,
			})
		}
	}
	return out
}

/* ---------- keys ---------- */

// resolveKey returns the API key for a provider: stored key → models.dev env
// var → legacy MODEL_API_KEY (meta only). Empty when none is available.
func (s *Service) resolveKey(ctx context.Context, p *CatalogProvider, row *models.LLMProvider) string {
	if row != nil && len(row.APIKeyEnc) > 0 {
		if plain, err := openKey(s.aesKey, row.APIKeyEnc); err == nil {
			return string(plain)
		} else {
			s.log.Error("llm: decrypt stored key", "provider", p.ID, "err", err)
		}
	}
	for _, env := range p.Env {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	if p.ID == "meta" && s.cfg.ModelAPIKey != "" {
		return s.cfg.ModelAPIKey
	}
	return ""
}

// hasKey reports whether a key is available for the provider (stored or env).
func (s *Service) hasKey(ctx context.Context, p *CatalogProvider, row *models.LLMProvider) bool {
	if row != nil && len(row.APIKeyEnc) > 0 {
		return true
	}
	for _, env := range p.Env {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return p.ID == "meta" && s.cfg.ModelAPIKey != ""
}

// SetKey stores (or clears, when empty) the provider's API key, encrypted at rest.
func (s *Service) SetKey(ctx context.Context, providerID, plaintext string) error {
	row, err := s.stateRow(ctx, providerID)
	if err != nil {
		return err
	}
	if row == nil {
		row = &models.LLMProvider{ProviderID: providerID, BaseURLVars: map[string]string{}}
	}
	if plaintext == "" {
		row.APIKeyEnc = nil
	} else {
		sealed, err := sealKey(s.aesKey, []byte(plaintext))
		if err != nil {
			return err
		}
		row.APIKeyEnc = sealed
	}
	if err := dbx.Conn(ctx, s.db).Save(row).Error; err != nil {
		return fmt.Errorf("llm: save provider key: %w", err)
	}
	return nil
}

/* ---------- settings API helpers ---------- */

// Snapshot builds the GET /settings/providers payload: catalog (snapshot
// order) joined with state rows. Keys are never returned — has_key only.
// Only AllowedProviders are listed (setup wizard scope, 2026-11-05).
func (s *Service) Snapshot(ctx context.Context) (ProvidersResponse, error) {
	provs, syncedAt := s.Catalog()
	catalog := make([]ProviderCatalogEntry, 0, len(provs))
	state := make([]ProviderState, 0, len(provs))
	for _, p := range provs {
		entry := ProviderCatalogEntry{
			ID:         p.ID,
			Name:       p.Name,
			NPM:        p.NPM,
			Adapter:    p.Adapter,
			API:        p.API,
			Doc:        p.Doc,
			ModelCount: len(p.Models),
		}
		if len(p.Env) > 0 {
			entry.Env = p.Env[0]
		}
		catalog = append(catalog, entry)

		row, err := s.stateRow(ctx, p.ID)
		if err != nil {
			return ProvidersResponse{}, err
		}
		state = append(state, s.stateDTO(p, row))
	}
	resp := ProvidersResponse{Catalog: catalog, State: state}
	if !syncedAt.IsZero() {
		ts := syncedAt.UTC().Format(time.RFC3339)
		resp.SyncedAt = &ts
	}
	return resp, nil
}

// stateDTO renders one provider's state row (has_key computed, key withheld).
func (s *Service) stateDTO(p *CatalogProvider, row *models.LLMProvider) ProviderState {
	vars := map[string]string{}
	rpm := 0
	if row != nil {
		for k, v := range row.BaseURLVars {
			vars[k] = v
		}
		rpm = row.RPM
	}
	st := ProviderState{
		ProviderID:  p.ID,
		Enabled:     row != nil && row.Enabled,
		HasKey:      s.hasKey(context.WithoutCancel(context.Background()), p, row),
		BaseURLVars: vars,
	}
	if rpm > 0 {
		st.RPMLimit = &rpm
	}
	return st
}

// SetupStatus reports whether first-run setup is still needed: true when no
// allowed provider is both enabled and has a key (stored or env fallback).
// The wizard (GET /settings/setup/status) redirects to /chat once false.
func (s *Service) SetupStatus(ctx context.Context) (needsSetup bool, enabledCount, readyCount int) {
	provs, _ := s.Catalog()
	for _, p := range provs {
		row, err := s.stateRow(ctx, p.ID)
		if err != nil || row == nil || !row.Enabled {
			continue
		}
		enabledCount++
		if s.hasKey(ctx, p, row) {
			readyCount++
		}
	}
	return readyCount == 0, enabledCount, readyCount
}

// UpdateProvider applies a PUT /settings/providers/{id} body and answers with
// the updated state. Unknown provider → 404; none adapter → 400; providers
// outside the setup-wizard allowlist (only openrouter + meta) → 400.
func (s *Service) UpdateProvider(ctx context.Context, providerID string, body updateProviderBody) (ProviderState, error) {
	if !IsAllowedProvider(providerID) {
		return ProviderState{}, apperr.Invalid("provider "+providerID+" is not available (only openrouter and meta can be enabled)", nil)
	}
	p, ok := s.CatalogEntry(providerID)
	if !ok {
		return ProviderState{}, apperr.NotFound("unknown provider " + providerID)
	}
	if p.Adapter == AdapterNone {
		return ProviderState{}, apperr.Invalid("provider uses an unsupported adapter and cannot be enabled", nil)
	}
	if body.RPMLimit != nil && *body.RPMLimit < 0 {
		return ProviderState{}, apperr.Invalid("validation failed", map[string]string{"rpm_limit": "must be >= 0"})
	}
	row, err := s.stateRow(ctx, providerID)
	if err != nil {
		return ProviderState{}, err
	}
	if row == nil {
		row = &models.LLMProvider{ProviderID: providerID, BaseURLVars: map[string]string{}}
	}
	row.Enabled = body.Enabled
	if body.BaseURLVars != nil {
		row.BaseURLVars = body.BaseURLVars
	}
	if body.RPMLimit != nil {
		row.RPM = *body.RPMLimit
	} else if row.BaseURLVars == nil {
		row.BaseURLVars = map[string]string{}
	}
	if body.APIKey != nil && *body.APIKey != "" {
		sealed, err := sealKey(s.aesKey, []byte(*body.APIKey))
		if err != nil {
			return ProviderState{}, err
		}
		row.APIKeyEnc = sealed
	}
	if err := dbx.Conn(ctx, s.db).Save(row).Error; err != nil {
		return ProviderState{}, fmt.Errorf("llm: save provider state: %w", err)
	}
	return s.stateDTO(p, row), nil
}

/* ---------- provider construction ---------- */

// Provider builds a ready-to-use llm.Provider for providerID, wrapped with the
// per-provider RPM limiter. Errors:
//   - unknown provider → NotFound
//   - adapter none/unsupported → Unavailable
//   - disabled → Unavailable("provider is not enabled")
//   - no key → Unavailable("no API key configured")
func (s *Service) Provider(ctx context.Context, providerID string) (llm.Provider, error) {
	p, ok := s.CatalogEntry(providerID)
	if !ok {
		return nil, apperr.NotFound("unknown provider " + providerID)
	}
	if p.Adapter == AdapterNone {
		return nil, apperr.Unavailable(fmt.Sprintf("provider %q uses an unsupported adapter", providerID))
	}
	row, err := s.stateRow(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if row == nil || !row.Enabled {
		return nil, apperr.Unavailable("provider is not enabled")
	}
	key := s.resolveKey(ctx, p, row)
	if key == "" {
		return nil, apperr.Unavailable("no API key configured")
	}
	tmpl, err := p.BaseURLTemplate()
	if err != nil {
		return nil, err
	}
	baseURL, err := BaseURL(tmpl, row.BaseURLVars)
	if err != nil {
		return nil, err
	}
	prov, err := s.buildAdapter(p, key, baseURL)
	if err != nil {
		return nil, err
	}
	if row.RPM > 0 {
		prov = &rateLimited{inner: prov, limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(row.RPM)), 1)}
	}
	return prov, nil
}

// TestProvider makes a tiny real request (1 output token) against the
// provider's first model and reports whether it succeeded, with a detail
// string on failure. Unconfigured providers report (false, reason) rather
// than an error.
func (s *Service) TestProvider(ctx context.Context, providerID string) (bool, string) {
	p, ok := s.CatalogEntry(providerID)
	if !ok {
		return false, "unknown provider"
	}
	if p.Adapter == AdapterNone {
		return false, "provider uses an unsupported adapter"
	}
	row, err := s.stateRow(ctx, providerID)
	if err != nil {
		return false, err.Error()
	}
	if row == nil || !row.Enabled {
		return false, "provider is not enabled"
	}
	if !s.hasKey(ctx, p, row) {
		return false, "no API key configured"
	}
	prov, err := s.Provider(ctx, providerID)
	if err != nil {
		return false, err.Error()
	}
	if len(p.Models) == 0 {
		return false, "provider has no chat models"
	}
	model := p.Models[0]
	req := &llm.Request{
		Model:           model.ID,
		Messages:        []llm.Message{llm.UserText("Say OK.")},
		MaxOutputTokens: 1,
	}
	for ev, err := range prov.Stream(ctx, req) {
		if err != nil {
			return false, err.Error()
		}
		if ev.Type == llm.StreamMessageDone {
			return true, ""
		}
	}
	return true, ""
}

// rateLimited caps provider throughput with a token-bucket limiter.
type rateLimited struct {
	inner   llm.Provider
	limiter *rate.Limiter
}

func (p *rateLimited) Name() string { return p.inner.Name() }

func (p *rateLimited) Stream(ctx context.Context, req *llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if err := p.limiter.Wait(ctx); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		p.inner.Stream(ctx, req)(func(ev llm.StreamEvent, err error) bool {
			return yield(ev, err)
		})
	}
}
