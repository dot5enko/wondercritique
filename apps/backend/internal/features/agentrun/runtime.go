// The agent runtime: builds runnable *agentkit.Agents from published (or
// draft, for Try) studio definitions, caches them per (agent def id, version),
// and resolves model sets and images.
package agentrun

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	akitllm "github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/sandbox/jsrun"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/sqlite"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	llmsvc "github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"gorm.io/gorm"
)

// Runtime builds and caches studio agents.
type Runtime struct {
	store      *studio.Store
	llm        *llmsvc.Service
	agentStore store.Store // shared sqlite agentkit store (chat sessions)
	blobs      *blobs.Store
	js         *jsrun.Runner

	vfsStore *sqlite.Store
	db       *sql.DB

	mu         sync.Mutex
	builds     map[string]*agentkit.Agent // "defID|version" -> built agent
	nodeBuilds map[string]*nodeBuild      // pipeline-node builds, see node.go
	material   map[string]*agentMaterial  // agent name -> read-only VFS content

	mountMu    sync.Mutex
	mounts     map[string]vfs.FS
	mountOrder []string

	// ephemeral maps a try-run session id to its agent name so sessionMount
	// can resolve mounts for sessions living in a private in-memory store.
	ephemeralMu sync.RWMutex
	ephemeral   map[string]string

	materialMu sync.RWMutex
	log        *slog.Logger

	// providerFn resolves providers; nil uses the llm Service (tests override it).
	providerFn func(ctx context.Context, providerID string) (akitllm.Provider, error)
}

// providerFor resolves a provider by id.
func (r *Runtime) providerFor(ctx context.Context, providerID string) (akitllm.Provider, error) {
	if r.providerFn != nil {
		return r.providerFn(ctx, providerID)
	}
	return r.llm.Provider(ctx, providerID)
}

// NewRuntime builds the runtime. The sqlite VFS store shares the app's DB.
func NewRuntime(defs *studio.Store, svc *llmsvc.Service, agentStore store.Store, db *gorm.DB, blobStore *blobs.Store, log *slog.Logger) (*Runtime, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("agentrun: sql db: %w", err)
	}
	vfsStore, err := sqlite.New(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("agentrun: vfs store: %w", err)
	}
	r := &Runtime{
		store:      defs,
		llm:        svc,
		agentStore: agentStore,
		blobs:      blobStore,
		js:         jsrun.New(jsrun.Config{}),
		log:        log,
		vfsStore:   vfsStore,
		builds:     map[string]*agentkit.Agent{},
		nodeBuilds: map[string]*nodeBuild{},
		material:   map[string]*agentMaterial{},
		mounts:     map[string]vfs.FS{},
		ephemeral:  map[string]string{},
	}
	// Compile the wasm module before the first run.
	if err := r.js.Warmup(context.Background()); err != nil {
		log.Warn("agentrun: js sandbox warmup failed", "err", err)
	}
	return r, nil
}

// logWriter returns the runtime's logger (for httpapi.Options).
func (r *Runtime) logWriter() *slog.Logger { return r.log }

/* ---------- body resolution ---------- */

// resolvedBody is a resolved agent definition body plus the def it came from.
type resolvedBody struct {
	Def  *models.StudioDef
	Body studio.AgentBody
}

// resolveAgent resolves the agent body: the draft when present (and allowed),
// else the latest published version. draftFirst is true for Try/preview (the
// editor flushes the draft before try/preview); the chat mount uses latest
// published only.
func (r *Runtime) resolveAgent(ctx context.Context, defID string, draftFirst bool) (*resolvedBody, error) {
	def, err := r.store.Get(ctx, studio.KindAgent, defID)
	if err != nil {
		return nil, err
	}
	if draftFirst && len(def.Draft) > 0 && string(def.Draft) != "null" {
		body, err := studio.DecodeBody[studio.AgentBody](def.Draft)
		if err != nil {
			return nil, err
		}
		return &resolvedBody{Def: def, Body: body}, nil
	}
	if def.LatestVer == nil {
		return nil, apperr.NotFound("no draft and no published version")
	}
	ver, err := r.store.Version(ctx, def.ID, *def.LatestVer)
	if err != nil {
		return nil, err
	}
	body, err := studio.DecodeBody[studio.AgentBody](ver.Body)
	if err != nil {
		return nil, err
	}
	return &resolvedBody{Def: def, Body: body}, nil
}

// resolveBySlug resolves a published agent by slug (chat mount).
func (r *Runtime) resolveBySlug(ctx context.Context, slug string) (*resolvedBody, error) {
	def, ver, err := r.store.LatestPublished(ctx, studio.KindAgent, slug)
	if err != nil {
		return nil, apperr.NotFound("no published agent " + slug).Wrap(err)
	}
	body, err := studio.DecodeBody[studio.AgentBody](ver.Body)
	if err != nil {
		return nil, err
	}
	return &resolvedBody{Def: def, Body: body}, nil
}

// resolveModelSet resolves the model set referenced by an agent body to one
// buildable entry. The referenced model_set's latest published body is used,
// falling back to its draft (test runs). Strategy single/fallback walk the
// entries on provider-build failure; fanout runs the first entry for now
// (parallel fanout is an engine concern, out of scope here).
func (r *Runtime) resolveModelSet(ctx context.Context, modelSetID string, draftFallback bool) (akitllm.Provider, *studio.ModelSetEntry, error) {
	def, err := r.store.Get(ctx, studio.KindModelSet, modelSetID)
	if err != nil {
		return nil, nil, apperr.Invalid(fmt.Sprintf("model set %q does not exist", modelSetID), nil).Wrap(err)
	}
	var body studio.ModelSetBody
	if def.LatestVer != nil {
		ver, err := r.store.Version(ctx, def.ID, *def.LatestVer)
		if err != nil {
			return nil, nil, err
		}
		body, err = studio.DecodeBody[studio.ModelSetBody](ver.Body)
		if err != nil {
			return nil, nil, err
		}
	} else if draftFallback && len(def.Draft) > 0 && string(def.Draft) != "null" {
		var err error
		body, err = studio.DecodeBody[studio.ModelSetBody](def.Draft)
		if err != nil {
			return nil, nil, err
		}
	} else {
		return nil, nil, apperr.Unavailable("model set has no published version")
	}
	if len(body.Entries) == 0 {
		return nil, nil, apperr.Unavailable("model set has no entries")
	}
	// Every strategy walks the entries in order and skips those whose provider
	// is unavailable (disabled, no key, …); the first buildable entry wins.
	// single/fanout then run just that entry (parallel fanout isn't implemented).
	var lastErr error
	var tried []string
	for _, entry := range body.Entries {
		provider, err := r.providerFor(ctx, entry.ProviderID)
		if err != nil {
			lastErr = err
			tried = append(tried, fmt.Sprintf("%s/%s: %s", entry.ProviderID, entry.ModelID, reasonOf(err)))
			continue
		}
		return provider, &entry, nil
	}
	if lastErr != nil {
		// Keep the original error code/status, but say which model set and
		// which provider/model failed, and where to fix it.
		msg := fmt.Sprintf("model set %q (%s): %s; enable the provider and set its API key under Providers, or pick another model in the model set",
			def.Slug, body.Strategy, strings.Join(tried, "; "))
		var ae *apperr.Error
		if errors.As(lastErr, &ae) {
			return nil, nil, apperr.New(ae.Status, ae.Code, msg)
		}
		return nil, nil, fmt.Errorf("%s: %w", msg, lastErr)
	}
	return nil, nil, apperr.Unavailable("no buildable model entry")
}

// reasonOf returns the client-safe message of an apperr, else the error text.
func reasonOf(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

/* ---------- agent construction ---------- */

// buildKey identifies a cached build.
func buildKey(defID string, version int) string {
	return fmt.Sprintf("%s|%d", defID, version)
}

// versionOf returns the def's latest published version (0 when unpublished/draft).
func versionOf(def *models.StudioDef) int {
	if def.LatestVer == nil {
		return 0
	}
	return *def.LatestVer
}

// buildAgent constructs the *agentkit.Agent for a resolved body and registers
// its VFS material under the agent name. sessStore is the store its sessions
// live in (the shared sqlite store, or an ephemeral in-memory one for try runs).
func (r *Runtime) buildAgent(ctx context.Context, res *resolvedBody, name string, sessStore store.Store) (*agentkit.Agent, error) {
	agent, _, err := r.buildAgentInfo(ctx, res, name, sessStore)
	return agent, err
}

// buildAgentInfo is buildAgent plus a description of the resolved model.
func (r *Runtime) buildAgentInfo(ctx context.Context, res *resolvedBody, name string, sessStore store.Store) (*agentkit.Agent, *buildInfo, error) {
	provider, entry, err := r.resolveModelSet(ctx, res.Body.ModelSetID, true)
	if err != nil {
		return nil, nil, err
	}
	info := &buildInfo{providerID: entry.ProviderID, modelID: entry.ModelID}
	if ms, err := r.store.Get(ctx, studio.KindModelSet, res.Body.ModelSetID); err == nil && ms.LatestVer != nil {
		info.modelSetVersion = *ms.LatestVer
	}

	ruleGroups, skills, err := r.loadRefs(ctx, res.Body)
	if err != nil {
		return nil, nil, err
	}
	promptSkills := make([]PromptSkill, len(skills))
	skillMounts := make([]skillMount, len(skills))
	for i, sk := range skills {
		promptSkills[i] = PromptSkill{Slug: sk.slug, Manifest: sk.manifest, Files: len(sk.files) - 1}
		files := map[string][]byte{sk.manifestPath: []byte(sk.manifest)}
		for p, content := range sk.files {
			if p != sk.manifestPath {
				files[p] = []byte(content)
			}
		}
		skillMounts[i] = skillMount{slug: sk.slug, files: files}
	}
	system := AssembleSystem(res.Body.SystemPrompt, ruleGroups, promptSkills, res.Body.OutputContract)
	tools, err := r.buildTools(res.Body)
	if err != nil {
		return nil, nil, err
	}

	maxSteps := res.Body.Limits.MaxSteps
	if maxSteps <= 0 {
		maxSteps = agentkit.DefaultMaxSteps
	}
	agent, err := agentkit.New(agentkit.Config{
		Name:            name,
		Provider:        provider,
		Model:           entry.ModelID,
		System:          system,
		Tools:           tools,
		Store:           sessStore,
		MaxSteps:        maxSteps,
		MaxOutputTokens: entryParam(entry.Params, func(p *studio.ModelParams) int { return p.MaxOutputTokens }),
		ReasoningEffort: entryEffort(entry.Params),
		Hooks: agentkit.Hooks{
			BeforeRequest: r.beforeRequest,
		},
	})
	if err != nil {
		return nil, nil, err
	}

	// Record the read-only VFS content under the agent name (sessions bind to names).
	mat := &agentMaterial{skills: skillMounts, rulesMD: rulesMarkdown(ruleGroups)}
	r.materialMu.Lock()
	r.material[name] = mat
	r.materialMu.Unlock()
	return agent, info, nil
}

func entryParam(p *studio.ModelParams, get func(*studio.ModelParams) int) int {
	if p == nil {
		return 0
	}
	return get(p)
}

func entryEffort(p *studio.ModelParams) string {
	if p == nil {
		return ""
	}
	return p.ReasoningEffort
}

// skillSource is one loaded skill definition.
type skillSource struct {
	slug         string
	manifestPath string // always SKILL.md
	manifest     string
	files        map[string]string // path -> content (including the manifest)
}

// loadRefs loads the agent's rule groups and skills from the referenced defs
// (latest published body, falling back to the draft). Missing references are
// tolerated (they were validated at publish time; defs may have been deleted).
func (r *Runtime) loadRefs(ctx context.Context, body studio.AgentBody) ([]studio.RuleGroup, []skillSource, error) {
	var groups []studio.RuleGroup
	for _, id := range body.RuleIDs {
		rb, err := r.defBody(ctx, studio.KindRule, id)
		if err != nil {
			continue
		}
		groups = append(groups, rb.Groups...)
	}
	var skills []skillSource
	for _, id := range body.SkillIDs {
		raw, err := r.defRawBody(ctx, studio.KindSkill, id)
		if err != nil {
			continue
		}
		sb, err := studio.DecodeBody[studio.SkillBody](raw)
		if err != nil {
			continue
		}
		sk := skillSource{slug: id, manifestPath: studio.SKILLFileName, files: map[string]string{}}
		// Use the def slug when available for a friendlier mount path.
		if def, err := r.store.Get(ctx, studio.KindSkill, id); err == nil {
			sk.slug = def.Slug
		}
		for _, f := range sb.Files {
			sk.files[f.Path] = f.Content
			if f.Path == studio.SKILLFileName {
				sk.manifest = f.Content
			}
		}
		if sk.manifest == "" {
			continue // a skill without SKILL.md cannot be attached
		}
		skills = append(skills, sk)
	}
	return groups, skills, nil
}

// defBody fetches and decodes a def's latest published body (draft fallback).
func (r *Runtime) defBody(ctx context.Context, kind, id string) (studio.RuleBody, error) {
	var out studio.RuleBody
	raw, err := r.defRawBody(ctx, kind, id)
	if err != nil {
		return out, err
	}
	body, err := studio.DecodeBody[studio.RuleBody](raw)
	if err != nil {
		return out, err
	}
	return body, nil
}

// defRawBody returns a def's latest published body, falling back to its draft.
func (r *Runtime) defRawBody(ctx context.Context, kind, id string) (json.RawMessage, error) {
	def, err := r.store.Get(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if def.LatestVer != nil {
		ver, err := r.store.Version(ctx, def.ID, *def.LatestVer)
		if err != nil {
			return nil, err
		}
		return ver.Body, nil
	}
	if len(def.Draft) > 0 && string(def.Draft) != "null" {
		return def.Draft, nil
	}
	return nil, apperr.NotFound("no published version")
}

// beforeRequest is the shared request hook: attribute the request to the
// session owner (sha256[:8], as the demo critic did) and resolve wcblob://
// image blocks to data: URLs.
func (r *Runtime) beforeRequest(ctx context.Context, sess *store.Session, req *akitllm.Request) error {
	if owner, ok := sess.Metadata["owner"].(string); ok && owner != "" {
		sum := sha256.Sum256([]byte(owner))
		req.SafetyID = hex.EncodeToString(sum[:8])
	}
	for i := range req.Messages {
		for j := range req.Messages[i].Blocks {
			b := &req.Messages[i].Blocks[j]
			if b.Type != akitllm.BlockImage || !strings.HasPrefix(b.ImageURL, "wcblob://") {
				continue
			}
			sha, err := blobs.ParseURL(b.ImageURL)
			if err != nil {
				return fmt.Errorf("agentrun: bad image url: %w", err)
			}
			dataURL, err := r.blobs.DataURL(ctx, sha)
			if err != nil {
				return fmt.Errorf("agentrun: resolve image: %w", err)
			}
			b.ImageURL = dataURL
		}
	}
	return nil
}

// Agent returns the built (and cached) agent for a resolved body. The cache
// key is the def id and version; Try builds (draft resolution) use version 0.
func (r *Runtime) Agent(ctx context.Context, res *resolvedBody, name string) (*agentkit.Agent, error) {
	key := buildKey(res.Def.ID, versionOf(res.Def))
	r.mu.Lock()
	if a, ok := r.builds[key]; ok {
		r.mu.Unlock()
		return a, nil
	}
	r.mu.Unlock()

	a, err := r.buildAgent(ctx, res, name, r.agentStore)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.builds[key] = a
	r.mu.Unlock()
	return a, nil
}

// EphemeralSession registers a try-run session id under its agent name so the
// session-scoped VFS resolves for sessions in the private in-memory store.
func (r *Runtime) EphemeralSession(sessionID, agentName string) {
	r.ephemeralMu.Lock()
	r.ephemeral[sessionID] = agentName
	r.ephemeralMu.Unlock()
}
