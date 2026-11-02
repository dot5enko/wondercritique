package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"gorm.io/gorm"
)

func apperrIsNotFound(err error) bool { return apperr.IsCode(err, apperr.CodeNotFound) }
func apperrIsInvalid(err error) bool  { return apperr.IsCode(err, apperr.CodeInvalid) }
func apperrIsUnavailable(err error) bool {
	return apperr.IsCode(err, apperr.CodeUnavailable)
}

func testConfig() *config.Config {
	return &config.Config{
		Env:        "dev",
		JWTSecret:  strings.Repeat("j", 40),
		SecretsKey: strings.Repeat("s", 40),
	}
}

func newTestService(t *testing.T, catalog http.HandlerFunc) (*Service, *gorm.DB) {
	t.Helper()
	db, err := dbx.Open(fmt.Sprintf("file:llm_%s?mode=memory&cache=shared", models.NewID()), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.LLMCatalog{}, &models.LLMProvider{}); err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	if catalog != nil {
		srv = httptest.NewServer(catalog)
		t.Cleanup(srv.Close)
	}
	cfg := testConfig()
	s, err := NewService(db, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if srv != nil {
		s.catURL = srv.URL
		s.http = &http.Client{Timeout: 5 * 1e9} // fast local client
	}
	return s, db
}

// fakeCatalog serves a small models.dev-shaped snapshot.
func fakeCatalog(etag string) http.HandlerFunc {
	body := fmt.Sprintf(`{
	  "meta": {"id":"meta","name":"Meta","env":["META_MODEL_API_KEY"],"npm":"@ai-sdk/openai",
	           "api":"https://api.meta.ai/v1","doc":"https://meta.ai",
	           "models":{
	             "muse-1": {"id":"muse-1","name":"Muse 1","tool_call":true,
	                        "modalities":{"input":["text","image"],"output":["text"]},
	                        "limit":{"context":1000,"output":100},
	                        "reasoning_options":[{"type":"effort","values":["low","high"]}]},
	             "muse-dep": {"id":"muse-dep","name":"Muse Dep","status":"deprecated"},
	             "muse-embed": {"id":"muse-embed","name":"Muse Embed","type":"embedding"}
	           }},
	  "compat": {"id":"compat","name":"Compat","env":["COMPAT_KEY"],"npm":"@ai-sdk/openai-compatible",
	             "api":"https://${REGION}.compat.example/v1",
	             "models":{"c-1":{"id":"c-1","name":"C1"}}},
	  "anthropic": {"id":"anthropic","name":"Anthropic","env":["ANTHROPIC_API_KEY"],"npm":"@ai-sdk/anthropic",
	               "models":{"c3":{"id":"c3","name":"Claude 3"}}},
	  "weird": {"id":"weird","name":"Weird","npm":"@ai-sdk/unknown",
	           "models":{"w-1":{"id":"w-1","name":"W1"}}},
	  "shaped": {"id":"shaped","name":"Shaped","npm":"@ai-sdk/openai-compatible",
	            "models":{
	              "s-resp": {"id":"s-resp","name":"S R","provider":{"shape":"responses"}},
	              "s-chat": {"id":"s-chat","name":"S C"}
	            }}
	}`)
	return func(w http.ResponseWriter, r *http.Request) {
		if etag != "" && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(body))
	}
}

func TestParseCatalogAndResolve(t *testing.T) {
	s, _ := newTestService(t, fakeCatalog(`"e1"`))
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	provs, syncedAt := s.Catalog()
	if len(provs) != 5 {
		t.Fatalf("providers = %d", len(provs))
	}
	if syncedAt.IsZero() {
		t.Error("synced_at unset")
	}
	byID := map[string]*CatalogProvider{}
	for _, p := range provs {
		byID[p.ID] = p
	}
	if byID["meta"].Adapter != AdapterResponses {
		t.Errorf("meta adapter = %q", byID["meta"].Adapter)
	}
	if len(byID["meta"].Models) != 2 { // embedding dropped
		t.Fatalf("meta models = %d", len(byID["meta"].Models))
	}
	m := byID["meta"].Models[0]
	if !m.Vision || !m.Tools || m.ContextWindow != 1000 || m.MaxOutput != 100 {
		t.Errorf("muse-1 = %+v", m)
	}
	if len(m.ReasoningOptions) != 2 || m.ReasoningOptions[0] != "low" {
		t.Errorf("reasoning options = %v", m.ReasoningOptions)
	}
	if !byID["meta"].Models[1].Deprecated {
		t.Error("muse-dep should be deprecated")
	}
	if byID["compat"].Adapter != AdapterChat {
		t.Errorf("compat adapter = %q", byID["compat"].Adapter)
	}
	if byID["anthropic"].Adapter != AdapterAnthropic {
		t.Errorf("anthropic adapter = %q", byID["anthropic"].Adapter)
	}
	if byID["weird"].Adapter != AdapterNone {
		t.Errorf("weird adapter = %q", byID["weird"].Adapter)
	}
	// shaped: s-resp differs from the provider adapter and is dropped; s-chat kept.
	if got := len(byID["shaped"].Models); got != 1 || byID["shaped"].Models[0].ID != "s-chat" {
		t.Errorf("shaped models = %+v", byID["shaped"].Models)
	}
	// Snapshot order preserved.
	if provs[0].ID != "meta" || provs[1].ID != "compat" {
		t.Errorf("order = %v", []string{provs[0].ID, provs[1].ID})
	}
}

func TestSyncTTLAndRefresh(t *testing.T) {
	s, _ := newTestService(t, fakeCatalog(`"e1"`))
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := s.SyncedAt()
	// Snapshot is fresh: Sync must not fetch again.
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.SyncedAt().Equal(first) {
		t.Error("fresh snapshot was refetched")
	}
	// Force refresh with a changed etag → 200 with the same body.
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.SyncedAt().Equal(first) {
		t.Error("refresh did not update synced_at")
	}
	// 304 path: same etag, body preserved.
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.CatalogEntry("meta"); !ok {
		t.Error("registry lost after 304")
	}
}

func TestSyncOfflineIsError(t *testing.T) {
	// Unreachable URL — Sync returns an error (startup only warns).
	s, _ := newTestService(t, nil)
	s.catURL = "http://127.0.0.1:1/catalog.json"
	if err := s.Sync(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if len(s.EnabledModels()) != 0 {
		t.Error("registry should be empty offline")
	}
}

func TestResolveAdapterTable(t *testing.T) {
	cases := []struct{ npm, shape, id, want string }{
		{"@ai-sdk/openai", "", "", AdapterResponses},
		{"@ai-sdk/openai-compatible", "", "", AdapterChat},
		{"@ai-sdk/anthropic", "", "", AdapterAnthropic},
		{"", "responses", "", AdapterResponses},
		{"", "completions", "", AdapterChat},
		{"@ai-sdk/openai-compatible", "responses", "", AdapterResponses}, // per-model override wins
		{"", "", "openrouter", AdapterChat},
		{"", "", "groq", AdapterChat},
		{"", "", "xai", AdapterChat},
		{"", "", "deepinfra", AdapterChat},
		{"", "", "togetherai", AdapterChat},
		{"", "", "cerebras", AdapterChat},
		{"", "", "mistral", AdapterChat},
		{"", "", "perplexity", AdapterChat},
		{"", "", "some-gateway", AdapterNone},
		{"@ai-sdk/azure", "", "azure", AdapterNone},
	}
	for _, c := range cases {
		if got := ResolveAdapter(c.npm, c.shape, c.id); got != c.want {
			t.Errorf("ResolveAdapter(%q,%q,%q) = %q, want %q", c.npm, c.shape, c.id, got, c.want)
		}
	}
}

func TestBaseURLSubstitution(t *testing.T) {
	got, err := BaseURL("https://${REGION}.x.example/v1", map[string]string{"REGION": "eu"})
	if err != nil || got != "https://eu.x.example/v1" {
		t.Errorf("got %q err %v", got, err)
	}
	if _, err := BaseURL("https://${REGION}.x.example/v1", nil); err == nil {
		t.Error("expected missing-var error")
	}
	got, err = BaseURL("https://plain.example/v1", nil)
	if err != nil || got != "https://plain.example/v1" {
		t.Errorf("plain: got %q err %v", got, err)
	}
}

func TestKeyEncryptionRoundTrip(t *testing.T) {
	cfg := testConfig()
	key, err := secretsKey(cfg.SecretsKey, "", true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealKey(key, []byte("sk-secret"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := openKey(key, sealed)
	if err != nil || string(plain) != "sk-secret" {
		t.Fatalf("round trip = %q, %v", plain, err)
	}
	// Tamper detection.
	sealed[0] ^= 0xff
	if _, err := openKey(key, sealed); err == nil {
		t.Error("tampered ciphertext decrypted")
	}
	// Derivation from JWT_SECRET in dev.
	k2, err := secretsKey("", "jwt-secret-value-at-least-32-bytes!!", true)
	if err != nil {
		t.Fatal(err)
	}
	plain2, err := openKey(k2, sealed)
	_ = plain2
	_ = err
	if err == nil {
		t.Error("different derivation should not decrypt")
	}
	// Short key refused.
	if _, err := secretsKey("short", "", true); err == nil {
		t.Error("short SECRETS_KEY accepted")
	}
	// Empty in prod refused.
	if _, err := secretsKey("", "jwt", false); err == nil {
		t.Error("empty SECRETS_KEY accepted in prod")
	}
}

func TestProviderStateCRUDAndGuards(t *testing.T) {
	s, _ := newTestService(t, fakeCatalog(`"e1"`))
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Unknown provider → 404-shaped error.
	if _, err := s.UpdateProvider(ctx, "nope", updateProviderBody{Enabled: true, BaseURLVars: map[string]string{}}); !apperrIsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	// none adapter → 400.
	if _, err := s.UpdateProvider(ctx, "weird", updateProviderBody{Enabled: true, BaseURLVars: map[string]string{}}); !apperrIsInvalid(err) {
		t.Fatalf("err = %v", err)
	}
	// Negative rpm → 400.
	if _, err := s.UpdateProvider(ctx, "meta", updateProviderBody{Enabled: true, BaseURLVars: map[string]string{}, RPMLimit: intPtr(-1)}); !apperrIsInvalid(err) {
		t.Fatalf("err = %v", err)
	}

	// Enable meta with a key.
	st, err := s.UpdateProvider(ctx, "meta", updateProviderBody{
		Enabled: true, APIKey: strPtr("sk-meta"), BaseURLVars: map[string]string{}, RPMLimit: intPtr(30),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.HasKey || st.RPMLimit == nil || *st.RPMLimit != 30 {
		t.Errorf("state = %+v", st)
	}

	// Provider() builds a responses adapter for meta.
	p, err := s.Provider(ctx, "meta")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "meta" {
		t.Errorf("name = %q", p.Name())
	}

	// Disabled provider → unavailable.
	if _, err := s.Provider(ctx, "compat"); !apperrIsUnavailable(err) {
		t.Errorf("err = %v", err)
	}
	// Enabled but keyless → unavailable.
	if _, err := s.UpdateProvider(ctx, "compat", updateProviderBody{Enabled: true, BaseURLVars: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Provider(ctx, "compat"); !apperrIsUnavailable(err) {
		t.Errorf("err = %v", err)
	}

	// EnabledModels lists enabled providers' chat models.
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Catalog) != 5 || len(snap.State) != 5 {
		t.Fatalf("snapshot catalog=%d state=%d", len(snap.Catalog), len(snap.State))
	}
	if snap.SyncedAt == nil || *snap.SyncedAt == "" {
		t.Error("synced_at missing")
	}
	models := s.EnabledModels()
	if len(models) != 3 { // meta 2 + compat 1 (weird disabled, anthropic disabled)
		t.Fatalf("enabled models = %+v", models)
	}
	if models[0].ProviderID != "meta" || models[0].ModelID != "muse-1" || !models[0].Vision {
		t.Errorf("model 0 = %+v", models[0])
	}
	if models[2].ProviderID != "compat" {
		t.Errorf("model 2 = %+v", models[2])
	}
}

func TestKeyResolutionChain(t *testing.T) {
	s, _ := newTestService(t, fakeCatalog(`"e1"`))
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, _ := s.CatalogEntry("meta")

	// Legacy MODEL_API_KEY fallback (env var name differs from models.dev env).
	s.cfg.ModelAPIKey = "legacy-key"
	t.Setenv("META_MODEL_API_KEY", "")
	row := &models.LLMProvider{ProviderID: "meta", Enabled: true, BaseURLVars: map[string]string{}}
	if got := s.resolveKey(ctx, p, row); got != "legacy-key" {
		t.Errorf("legacy fallback = %q", got)
	}
	// models.dev env var wins over legacy.
	t.Setenv("META_MODEL_API_KEY", "env-key")
	if got := s.resolveKey(ctx, p, row); got != "env-key" {
		t.Errorf("env fallback = %q", got)
	}
	// Stored key wins over both.
	sealed, _ := sealKey(s.aesKey, []byte("stored-key"))
	row.APIKeyEnc = sealed
	if got := s.resolveKey(ctx, p, row); got != "stored-key" {
		t.Errorf("stored key = %q", got)
	}
}

func TestTestProviderNoKey(t *testing.T) {
	s, _ := newTestService(t, fakeCatalog(`"e1"`))
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.UpdateProvider(ctx, "meta", updateProviderBody{Enabled: true, BaseURLVars: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	ok, detail := s.TestProvider(ctx, "meta")
	if ok || detail != "no API key configured" {
		t.Errorf("ok=%v detail=%q", ok, detail)
	}
	ok, detail = s.TestProvider(ctx, "unknown")
	if ok || detail != "unknown provider" {
		t.Errorf("ok=%v detail=%q", ok, detail)
	}
}

func TestCatalogFetchSendsEtag(t *testing.T) {
	var seen string
	s, _ := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"v9"`)
		_, _ = w.Write([]byte(`{"meta":{"id":"meta","name":"Meta","models":{}}}`))
	})
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Errorf("first sync sent etag %q", seen)
	}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen == "" {
		t.Error("refresh did not send If-None-Match")
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
