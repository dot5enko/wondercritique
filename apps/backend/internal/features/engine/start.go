package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// Viewer is the caller: runs are visible to their owner and to admins.
type Viewer struct {
	UserID string
	Admin  bool
}

// StartParams are the validated POST /runs inputs.
type StartParams struct {
	Viewer
	PipelineVersionID string
	PipelineID        string
	// Agent (id or slug) runs one chat agent instead of a pipeline.
	Agent    string
	ThreadID string
	IsTest            bool
	// Input values are JSON strings or {"artifact_id": "..."} objects.
	Input map[string]json.RawMessage
	// ContextPaths are canvas files or folders of the thread the message is about.
	ContextPaths []string
}

const (
	maxInputText   = 100_000
	maxInputImage  = 8 << 20
	maxContextHTML = 60_000
)

var inputKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)

// Start validates the request, pins the pipeline, persists a pending run and
// starts executing it in the background.
func (e *Engine) Start(ctx context.Context, p StartParams) (*models.Run, error) {
	if p.IsTest && !p.Admin {
		return nil, apperr.Forbidden("test runs are limited to admins")
	}
	var (
		def   *models.StudioDef // the pipeline (pipeline mode)
		ver   *models.StudioDefVersion
		agent *models.StudioDef // the chat agent (agent mode)
		body  studio.PipelineBody
		err   error
	)
	if p.Agent != "" {
		if p.PipelineID != "" || p.PipelineVersionID != "" {
			return nil, apperr.Invalid("pass either agent or a pipeline", map[string]string{"agent": "cannot be combined with a pipeline"})
		}
		if agent, body, err = e.resolveChatAgent(ctx, p); err != nil {
			return nil, err
		}
	} else {
		if def, ver, body, err = e.resolvePipeline(ctx, p); err != nil {
			return nil, err
		}
		if issues := e.validatePipeline(ctx, body); len(issues) > 0 {
			return nil, apperr.Invalid("the pipeline is invalid: "+strings.Join(issues, "; "), nil)
		}
	}
	g, err := buildGraph(body)
	if err != nil {
		return nil, apperr.Invalid("the pipeline is invalid: "+err.Error(), nil)
	}
	input, err := e.resolveInput(ctx, p, body)
	if err != nil {
		return nil, err
	}

	run := &models.Run{
		Base:     models.Base{ID: models.NewID()},
		OwnerID:  p.UserID,
		ThreadID: p.ThreadID,
		Mode:     models.RunModePipeline,
		IsTest:   p.IsTest,
		Status:   models.RunPending,
	}
	if agent != nil {
		run.Mode, run.AgentID, run.PipelineName = models.RunModeAgent, agent.ID, agent.Name
	} else {
		run.PipelineID, run.PipelineSlug, run.PipelineName = def.ID, def.Slug, def.Name
	}
	if p.ThreadID == "" {
		run.ThreadID = run.ID
	} else if err := e.checkThread(ctx, p); err != nil {
		return nil, err
	}
	cr, folder, err := e.resolveCanvas(ctx, p, run.ThreadID, briefOf(input))
	if err != nil {
		return nil, err
	}
	run.CanvasFolder = folder
	for _, f := range cr.selected {
		run.ContextPaths = append(run.ContextPaths, f.Path)
	}
	if ver != nil {
		v := ver.Version
		run.PipelineVersion = &v
		run.PipelineVersionID = ver.ID
	}
	if run.Snapshot, err = json.Marshal(body); err != nil {
		return nil, apperr.Internal(err)
	}
	if run.Input, err = json.Marshal(input); err != nil {
		return nil, apperr.Internal(err)
	}
	// The run takes the thread's pending canvas notes in the same transaction,
	// so each note reaches exactly one run.
	var notes []models.CanvasNote
	err = dbx.Tx(ctx, e.DB, func(ctx context.Context) error {
		if e.Canvas != nil && p.ThreadID != "" {
			var err error
			if notes, err = e.Canvas.TakeNotes(ctx, run.ThreadID); err != nil {
				return err
			}
			for _, n := range notes {
				run.CanvasNotes = append(run.CanvasNotes, n.Text)
			}
		}
		return dbx.Conn(ctx, e.DB).Create(run).Error
	})
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("engine: create run: %w", err))
	}
	cr.seed = e.buildCanvasSeed(ctx, run, cr, notes)
	if run.Mode == models.RunModeAgent {
		cr.history = e.buildHistory(ctx, run)
	}
	// The background goroutine owns *run from here on; callers get a snapshot.
	snapshot := *run
	if err := e.launch(run, g, input, cr); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// validatePipeline returns the error-severity issues of the pipeline body.
func (e *Engine) validatePipeline(ctx context.Context, body studio.PipelineBody) []string {
	if e.LLM == nil {
		return nil
	}
	cat, err := studio.BuildCatalogData(ctx, e.Defs, e.LLM)
	if err != nil {
		e.Log.Warn("engine: catalog for validation", "err", err)
		return nil
	}
	var out []string
	for _, is := range studio.ValidatePipelineBody(ctx, body, cat) {
		if is.Severity == "error" {
			if is.NodeID != "" {
				out = append(out, is.NodeID+": "+is.Message)
			} else {
				out = append(out, is.Message)
			}
		}
	}
	return out
}

/* ---------- pipeline resolution ---------- */

// resolvePipeline pins the pipeline to run: an explicit version, an explicit
// pipeline (latest published, or its draft for admin test runs) or the default.
func (e *Engine) resolvePipeline(ctx context.Context, p StartParams) (*models.StudioDef, *models.StudioDefVersion, studio.PipelineBody, error) {
	var zero studio.PipelineBody

	var def *models.StudioDef
	var ver *models.StudioDefVersion
	switch {
	case p.PipelineVersionID != "":
		var v models.StudioDefVersion
		if err := e.DB.WithContext(ctx).First(&v, "id = ?", p.PipelineVersionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, zero, apperr.NotFound("pipeline version not found")
			}
			return nil, nil, zero, apperr.Internal(err)
		}
		d, err := e.Defs.Get(ctx, studio.KindPipeline, v.DefID)
		if err != nil {
			return nil, nil, zero, apperr.NotFound("pipeline version not found").Wrap(err)
		}
		def, ver = d, &v
	case p.PipelineID != "":
		d, err := e.Defs.Get(ctx, studio.KindPipeline, p.PipelineID)
		if err != nil {
			return nil, nil, zero, apperr.NotFound("pipeline not found").Wrap(err)
		}
		def = d
	default:
		d, _, err := e.Defs.LatestPublished(ctx, studio.KindPipeline, "default")
		if err != nil {
			pubs, perr := e.Defs.Published(ctx, studio.KindPipeline)
			if perr != nil || len(pubs) == 0 {
				return nil, nil, zero, apperr.Unavailable("no pipeline is published yet")
			}
			d = &pubs[0]
		}
		def = d
	}

	// Test runs by admins may use the pipeline's draft.
	if ver == nil && p.IsTest && p.Admin && len(def.Draft) > 0 && string(def.Draft) != "null" {
		body, err := studio.DecodeBody[studio.PipelineBody](def.Draft)
		if err != nil {
			return nil, nil, zero, err
		}
		return def, nil, body, nil
	}
	if ver == nil {
		if def.LatestVer == nil {
			return nil, nil, zero, apperr.Invalid("the pipeline has no published version", nil)
		}
		v, err := e.Defs.Version(ctx, def.ID, *def.LatestVer)
		if err != nil {
			return nil, nil, zero, err
		}
		ver = v
	}
	body, err := studio.DecodeBody[studio.PipelineBody](ver.Body)
	if err != nil {
		return nil, nil, zero, err
	}
	return def, ver, body, nil
}

// resolveChatAgent resolves the agent of an agent-mode run (id or slug) and
// synthesizes its graph: Input → Agent. The agent is pinned to its latest
// published version; admin test runs use its draft.
func (e *Engine) resolveChatAgent(ctx context.Context, p StartParams) (*models.StudioDef, studio.PipelineBody, error) {
	var zero studio.PipelineBody
	def, err := e.Defs.Get(ctx, studio.KindAgent, p.Agent)
	if err != nil {
		if def, err = e.Defs.GetBySlug(ctx, studio.KindAgent, p.Agent); err != nil {
			return nil, zero, apperr.NotFound("agent not found")
		}
	}
	cfg := map[string]any{"label": def.Name, "agent_id": def.ID}
	draft := p.IsTest && p.Admin && len(def.Draft) > 0 && string(def.Draft) != "null"
	if !draft {
		if def.LatestVer == nil {
			return nil, zero, apperr.Invalid("the agent has no published version", nil)
		}
		cfg["agent_version"] = float64(*def.LatestVer)
	}
	return def, chatGraph(cfg), nil
}

// chatGraph is the graph of an agent-mode run: the message goes to one agent.
func chatGraph(agentCfg map[string]any) studio.PipelineBody {
	return studio.PipelineBody{
		Nodes: []studio.PipelineNode{
			{ID: "input", Type: "input", Config: map[string]any{"label": "Message"}, Position: studio.NodePosition{X: 0, Y: 0}},
			{ID: "chat", Type: "agent", Config: agentCfg, Position: studio.NodePosition{X: 260, Y: 0}},
		},
		Edges: []studio.PipelineEdge{
			{ID: "input:brief->chat:brief", From: studio.EdgeEndpoint{Node: "input", Port: "brief"}, To: studio.EdgeEndpoint{Node: "chat", Port: "brief"}},
		},
	}
}

/* ---------- input ---------- */

// inputField is one declared field of an Input node.
type inputField struct {
	Key      string
	Type     string
	Required bool
}

// inputFields reads the Input nodes' declared form fields.
func inputFields(body studio.PipelineBody) []inputField {
	var out []inputField
	for _, n := range body.Nodes {
		if n.Type != "input" {
			continue
		}
		list, _ := n.Config["fields"].([]any)
		for _, f := range list {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			key, _ := m["key"].(string)
			if key == "" {
				continue
			}
			typ, _ := m["type"].(string)
			out = append(out, inputField{Key: key, Type: typ, Required: boolCfg(m, "required", true)})
		}
	}
	return out
}

// resolveInput turns the request's input values into strings: text stays text,
// data: images are stored as blobs (wcblob:// refs), {artifact_id} references
// resolve to the artifact's blob.
func (e *Engine) resolveInput(ctx context.Context, p StartParams, body studio.PipelineBody) (map[string]any, error) {
	out := make(map[string]any, len(p.Input))
	fields := map[string]string{}
	for k, raw := range p.Input {
		if !inputKeyRe.MatchString(k) {
			return nil, apperr.Invalid("invalid input", map[string]string{k: "invalid field name"})
		}
		v, err := e.resolveInputValue(ctx, p.Viewer, raw)
		if err != nil {
			fields[k] = err.Error()
			continue
		}
		if v != "" {
			out[k] = v
		}
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid input", fields)
	}
	missing := map[string]string{}
	for _, f := range inputFields(body) {
		if f.Required {
			if _, ok := out[f.Key]; !ok {
				missing[f.Key] = "is required"
			}
		}
	}
	if len(missing) > 0 {
		return nil, apperr.Invalid("missing input", missing)
	}
	if len(out) == 0 {
		return nil, apperr.Invalid("the run needs an input", map[string]string{"brief": "is required"})
	}
	return out, nil
}

func (e *Engine) resolveInputValue(ctx context.Context, v Viewer, raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return e.storeInputString(ctx, strings.TrimSpace(s))
	}
	var ref struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil || ref.ArtifactID == "" {
		return "", errors.New("must be a string or {artifact_id}")
	}
	art, run, err := e.artifactFor(ctx, ref.ArtifactID, v)
	if err != nil {
		return "", errors.New("artifact not found")
	}
	_ = run
	if art.Kind == models.ArtifactImage {
		return blobs.URL(art.BlobSHA), nil
	}
	b, err := e.Blobs.Get(ctx, art.BlobSHA)
	if err != nil {
		return "", errors.New("artifact content is unavailable")
	}
	return truncate(string(b.Data), maxInputText), nil
}

// storeInputString validates a text value; data:image URLs become blobs.
func (e *Engine) storeInputString(ctx context.Context, s string) (string, error) {
	switch {
	case strings.HasPrefix(s, "data:image/"):
		head, payload, ok := strings.Cut(s, ",")
		if !ok || !strings.HasSuffix(head, ";base64") {
			return "", errors.New("images must be base64 data URLs")
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64")
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", errors.New("invalid base64 image")
		}
		if len(data) > maxInputImage {
			return "", errors.New("image is too large")
		}
		sha, err := e.Blobs.Put(ctx, data, mime)
		if err != nil {
			return "", errors.New("invalid image")
		}
		return blobs.URL(sha), nil
	case strings.HasPrefix(s, "wcblob://"):
		sha, err := blobs.ParseURL(s)
		if err != nil {
			return "", errors.New("invalid image reference")
		}
		ok, err := e.Blobs.Exists(ctx, sha)
		if err != nil || !ok {
			return "", errors.New("unknown image reference")
		}
		return s, nil
	}
	if len(s) > maxInputText {
		return "", fmt.Errorf("must be at most %d characters", maxInputText)
	}
	return s, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

/* ---------- threads ---------- */

// checkThread verifies the caller may continue the thread.
func (e *Engine) checkThread(ctx context.Context, p StartParams) error {
	q := e.DB.WithContext(ctx).Model(&models.Run{}).Where("thread_id = ?", p.ThreadID)
	if !p.Admin {
		q = q.Where("owner_id = ?", p.UserID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return apperr.Internal(err)
	}
	if n == 0 {
		return apperr.NotFound("chat thread not found")
	}
	return nil
}

// briefOf picks the main text of a run input: `brief`, else the first
// (alphabetical) text value.
func briefOf(in map[string]any) string {
	if s, ok := in["brief"].(string); ok && !isImageRef(s) {
		return s
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := in[k].(string); ok && !isImageRef(s) && s != "" {
			return s
		}
	}
	return ""
}

func isImageRef(s string) bool {
	return strings.HasPrefix(s, "wcblob://") || strings.HasPrefix(s, "data:image/")
}
