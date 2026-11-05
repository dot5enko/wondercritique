package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/go-chi/chi/v5"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testIssuer = "wondercritique-test"

type harness struct {
	t       *testing.T
	router  chi.Router
	store   *Store
	mw      *auth.Middleware
	headers string
	catalog *CatalogData
}

func setup(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	db, err := dbx.Open(fmt.Sprintf("file:studio_%s?mode=memory&cache=shared", models.NewID()), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.StudioDef{}, &models.StudioDefVersion{}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	cfg := &config.Config{Env: "dev", JWTSecret: strings.Repeat("j", 40), SecretsKey: strings.Repeat("s", 40)}
	svc, err := llm.NewService(db, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", testIssuer)
	r := chi.NewRouter()
	NewController(store, svc, mw).Routes(r)

	token, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: "admin1"}, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, router: r, store: store, mw: mw, headers: "Bearer " + token}
}

func (h *harness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", h.headers)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func (h *harness) get(t *testing.T, path string, out any) {
	t.Helper()
	w := h.do(http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: bad json: %v", path, err)
	}
}

/* ---------- bodies used across tests ---------- */

// okModelSet returns a fresh valid model_set body (tests mutate their copies,
// so the Entries backing array must never be shared).
func okModelSet() ModelSetBody {
	return ModelSetBody{Strategy: "single", Entries: []ModelSetEntry{{ProviderID: "meta", ModelID: "muse-spark-1.3-contributor"}}}
}

func okAgent(modelSetID string) AgentBody {
	return AgentBody{
		ModelSetID:     modelSetID,
		SystemPrompt:   "sys",
		InputTemplate:  "{{brief}}",
		Tools:          []string{"read_file"},
		OutputContract: OutputContract{Type: "artifact"},
	}
}

var okSkill = SkillBody{Files: []SkillFile{{Path: "SKILL.md", Content: "# Skill"}}}

var okRule = RuleBody{Groups: []RuleGroup{{Name: "G", Rules: []RuleItem{{Text: "t", Severity: "error"}}}}}

var okPipeline = PipelineBody{
	Nodes: []PipelineNode{
		{ID: "n1", Type: "input", Config: map[string]any{}, Position: NodePosition{}},
		{ID: "n2", Type: "output", Config: map[string]any{}, Position: NodePosition{}},
	},
	Edges: []PipelineEdge{
		{ID: "e1", From: EdgeEndpoint{Node: "n1", Port: "brief"}, To: EdgeEndpoint{Node: "n2", Port: "html"}},
	},
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// draftCleared reports whether a def's draft is absent (the JSON null that a
// nil sql value round-trips to counts as cleared).
func draftCleared(def models.StudioDef) bool {
	return len(def.Draft) == 0 || string(def.Draft) == "null"
}

/* ---------- CRUD + versions + restore ---------- */

func TestCRUDVersionsRestoreRoundTrip(t *testing.T) {
	h := setup(t)

	// Create.
	w := h.do(http.MethodPost, "/studio/model_set", `{"slug":"main","name":"Main","description":"d","draft":`+marshal(t, okModelSet())+`}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	var def models.StudioDef
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Slug != "main" || def.Draft == nil || def.LatestVer != nil {
		t.Fatalf("def = %+v", def)
	}

	// Duplicate slug → 409.
	if w := h.do(http.MethodPost, "/studio/model_set", `{"slug":"main","name":"Main","draft":`+marshal(t, okModelSet())+`}`); w.Code != http.StatusConflict {
		t.Fatalf("dup slug = %d, want 409", w.Code)
	}

	// Draft update.
	updated := okModelSet()
	updated.Entries = append(updated.Entries, ModelSetEntry{ProviderID: "meta", ModelID: "muse-wonder-1.0"})
	if w := h.do(http.MethodPut, "/studio/model_set/"+def.ID+"/draft", marshal(t, updated)); w.Code != http.StatusOK {
		t.Fatalf("draft = %d %s", w.Code, w.Body)
	}

	// Publish requires draft; two versions (each publish needs a fresh draft).
	for _, note := range []string{"first", "second"} {
		if w := h.do(http.MethodPost, "/studio/model_set/"+def.ID+"/publish", `{"note":"`+note+`"}`); w.Code != http.StatusOK {
			t.Fatalf("publish %s = %d %s", note, w.Code, w.Body)
		}
		if note == "first" {
			var after models.StudioDef
			h.get(t, "/studio/model_set/"+def.ID, &after)
			if !draftCleared(after) || after.LatestVer == nil || *after.LatestVer != 1 {
				t.Fatalf("def after first publish = %+v", after)
			}
		}
		if w := h.do(http.MethodPut, "/studio/model_set/"+def.ID+"/draft", marshal(t, updated)); w.Code != http.StatusOK {
			t.Fatalf("re-draft = %d %s", w.Code, w.Body)
		}
	}
	// Clear the draft left by the re-draft above so the final assertion holds.
	if w := h.do(http.MethodPost, "/studio/model_set/"+def.ID+"/publish", `{"note":"third"}`); w.Code != http.StatusOK {
		t.Fatalf("publish third = %d %s", w.Code, w.Body)
	}
	var versions struct {
		Items []models.StudioDefVersion `json:"items"`
	}
	h.get(t, "/studio/model_set/"+def.ID+"/versions", &versions)
	if len(versions.Items) != 3 || versions.Items[0].Version != 3 {
		t.Fatalf("versions = %+v", versions.Items)
	}

	// Draft cleared after the last publish.
	var def2 models.StudioDef
	h.get(t, "/studio/model_set/"+def.ID, &def2)
	if !draftCleared(def2) || def2.LatestVer == nil || *def2.LatestVer != 3 {
		t.Fatalf("def after publish = %+v", def2)
	}

	// Restore v1 → draft reappears with v1 content.
	if w := h.do(http.MethodPost, "/studio/model_set/"+def.ID+"/restore/1", ""); w.Code != http.StatusOK {
		t.Fatalf("restore = %d %s", w.Code, w.Body)
	}
	var def3 models.StudioDef
	h.get(t, "/studio/model_set/"+def.ID, &def3)
	var body ModelSetBody
	if err := json.Unmarshal(def3.Draft, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("restored draft = %s", def3.Draft)
	}

	// List + limit/offset.
	var list struct {
		Items []models.StudioDef `json:"items"`
	}
	h.get(t, "/studio/model_set", &list)
	if len(list.Items) != 1 {
		t.Fatalf("list = %d", len(list.Items))
	}
	// Unknown kind → 404.
	if w := h.do(http.MethodGet, "/studio/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown kind = %d", w.Code)
	}
	// 404 on missing id.
	if w := h.do(http.MethodGet, "/studio/model_set/missing", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing id = %d", w.Code)
	}
}

/* ---------- per-kind validation ---------- */

func TestValidationTables(t *testing.T) {
	h := setup(t)

	// A model_set for agent reference checks.
	if w := h.do(http.MethodPost, "/studio/model_set", `{"slug":"main","name":"Main","draft":`+marshal(t, okModelSet())+`}`); w.Code != http.StatusCreated {
		t.Fatalf("seed model_set: %d %s", w.Code, w.Body)
	}
	var list struct {
		Items []models.StudioDef `json:"items"`
	}
	w := h.do(http.MethodGet, "/studio/model_set", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	ms := list.Items[0]

	post := func(kind, slug string, body any) *httptest.ResponseRecorder {
		return h.do(http.MethodPost, "/studio/"+kind, `{"slug":"`+slug+`","name":"T","draft":`+marshal(t, body)+`}`)
	}

	// model_set
	bad := okModelSet()
	bad.Strategy = "nope"
	if w := post("model_set", "t1", bad); w.Code != http.StatusBadRequest {
		t.Errorf("bad strategy = %d", w.Code)
	}
	empty := okModelSet()
	empty.Entries = nil
	// An empty set is a valid draft; publishing it is rejected.
	w = post("model_set", "t2", empty)
	if w.Code != http.StatusCreated {
		t.Fatalf("no entries draft = %d %s", w.Code, w.Body)
	}
	var emptyDef models.StudioDef
	if err := json.Unmarshal(w.Body.Bytes(), &emptyDef); err != nil {
		t.Fatal(err)
	}
	if w := h.do(http.MethodPost, "/studio/model_set/"+emptyDef.ID+"/publish", `{"note":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("publish with no entries = %d", w.Code)
	}
	neg := okModelSet()
	neg.Entries[0].Params = &ModelParams{MaxOutputTokens: -1}
	if w := post("model_set", "t3", neg); w.Code != http.StatusBadRequest {
		t.Errorf("negative max tokens = %d", w.Code)
	}

	// agent
	badAgent := okAgent(ms.ID)
	badAgent.ModelSetID = "missing"
	if w := post("agent", "a1", badAgent); w.Code != http.StatusBadRequest {
		t.Errorf("missing model_set = %d", w.Code)
	}
	custom := okAgent(ms.ID)
	custom.OutputContract = OutputContract{Type: "custom"}
	if w := post("agent", "a2", custom); w.Code != http.StatusBadRequest {
		t.Errorf("custom without schema = %d", w.Code)
	}
	custom.OutputContract = OutputContract{Type: "custom", FieldsSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)}
	if w := post("agent", "a3", custom); w.Code != http.StatusCreated {
		t.Errorf("custom with schema = %d %s", w.Code, w.Body)
	}
	unkTool := okAgent(ms.ID)
	unkTool.Tools = []string{"teleport"}
	if w := post("agent", "a4", unkTool); w.Code != http.StatusBadRequest {
		t.Errorf("unknown tool = %d", w.Code)
	}
	if w := post("agent", "a5", okAgent(ms.ID)); w.Code != http.StatusCreated {
		t.Errorf("valid agent = %d %s", w.Code, w.Body)
	}

	// skill
	noMD := SkillBody{Files: []SkillFile{{Path: "a.md", Content: "x"}}}
	if w := post("skill", "s1", noMD); w.Code != http.StatusBadRequest {
		t.Errorf("skill without SKILL.md = %d", w.Code)
	}
	dupPath := SkillBody{Files: []SkillFile{{Path: "SKILL.md", Content: "x"}, {Path: "SKILL.md", Content: "y"}}}
	if w := post("skill", "s2", dupPath); w.Code != http.StatusBadRequest {
		t.Errorf("duplicate path = %d", w.Code)
	}
	escape := SkillBody{Files: []SkillFile{{Path: "../evil.md", Content: "x"}, {Path: "SKILL.md", Content: "x"}}}
	if w := post("skill", "s3", escape); w.Code != http.StatusBadRequest {
		t.Errorf("path escape = %d", w.Code)
	}
	abs := SkillBody{Files: []SkillFile{{Path: "/abs.md", Content: "x"}, {Path: "SKILL.md", Content: "x"}}}
	if w := post("skill", "s4", abs); w.Code != http.StatusBadRequest {
		t.Errorf("absolute path = %d", w.Code)
	}
	if w := post("skill", "s5", okSkill); w.Code != http.StatusCreated {
		t.Errorf("valid skill = %d %s", w.Code, w.Body)
	}

	// rule
	noGroups := RuleBody{}
	if w := post("rule", "r1", noGroups); w.Code != http.StatusBadRequest {
		t.Errorf("no groups = %d", w.Code)
	}
	badSev := RuleBody{Groups: []RuleGroup{{Name: "G", Rules: []RuleItem{{Text: "t", Severity: "loud"}}}}}
	if w := post("rule", "r2", badSev); w.Code != http.StatusBadRequest {
		t.Errorf("bad severity = %d", w.Code)
	}
	if w := post("rule", "r3", okRule); w.Code != http.StatusCreated {
		t.Errorf("valid rule = %d %s", w.Code, w.Body)
	}

	// pipeline: draft accepts anything structural, publish validates the graph.
	messy := PipelineBody{Nodes: []PipelineNode{{ID: "a", Type: "input"}}, Edges: []PipelineEdge{
		{ID: "e", From: EdgeEndpoint{Node: "a", Port: "brief"}, To: EdgeEndpoint{Node: "ghost", Port: "html"}},
	}}
	if w := h.do(http.MethodPost, "/studio/pipeline", `{"slug":"t","name":"T","draft":`+marshal(t, messy)+`}`); w.Code != http.StatusCreated {
		t.Errorf("structural-only draft = %d %s", w.Code, w.Body)
	}
	var plist struct {
		Items []models.StudioDef `json:"items"`
	}
	_ = json.Unmarshal(h.do(http.MethodGet, "/studio/pipeline", "").Body.Bytes(), &plist)
	var pipe models.StudioDef
	for _, d := range plist.Items {
		if d.Slug == "t" {
			pipe = d
		}
	}
	if w := h.do(http.MethodPost, "/studio/pipeline/"+pipe.ID+"/publish", `{"note":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("publish broken graph = %d %s", w.Code, w.Body)
	}
	if w := h.do(http.MethodPost, "/studio/pipeline", `{"slug":"ok","name":"T","draft":`+marshal(t, okPipeline)+`}`); w.Code != http.StatusCreated {
		t.Errorf("valid pipeline = %d %s", w.Code, w.Body)
	}

	// Slug + name validation.
	if w := h.do(http.MethodPost, "/studio/rule", `{"slug":"Bad_Slug","name":"T","draft":`+marshal(t, okRule)+`}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad slug = %d", w.Code)
	}
	if w := h.do(http.MethodPost, "/studio/rule", `{"slug":"ok2","name":"","draft":`+marshal(t, okRule)+`}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty name = %d", w.Code)
	}
}

func TestPipelineValidation(t *testing.T) {
	cat := &CatalogData{NodeTypes: catalogNodeTypes(), Agents: []StudioRef{{ID: "x", Slug: "x", Name: "X"}}}

	// Loop edge from a non-gate node.
	body := PipelineBody{
		Nodes: []PipelineNode{
			{ID: "a", Type: "input"},
			{ID: "b", Type: "agent", Config: map[string]any{"agent_id": "x"}},
		},
		Edges: []PipelineEdge{
			{ID: "e1", From: EdgeEndpoint{Node: "a", Port: "brief"}, To: EdgeEndpoint{Node: "b", Port: "brief"}},
			{ID: "e2", From: EdgeEndpoint{Node: "b", Port: "html"}, To: EdgeEndpoint{Node: "a", Port: "brief"}, Loop: true},
		},
	}
	issues := ValidatePipelineBody(context.Background(), body, cat)
	if len(issues) == 0 {
		t.Fatal("expected issues")
	}
	// loop from non-gate + port-type error (text->text is fine; the loop source is the error).
	found := false
	for _, is := range issues {
		if strings.Contains(is.Message, "loop edge") {
			found = true
		}
	}
	if !found {
		t.Errorf("issues = %+v", issues)
	}

	// Type mismatch.
	mismatch := PipelineBody{
		Nodes: []PipelineNode{
			{ID: "a", Type: "input"},
			{ID: "b", Type: "output"},
		},
		Edges: []PipelineEdge{
			{ID: "e1", From: EdgeEndpoint{Node: "a", Port: "brief"}, To: EdgeEndpoint{Node: "b", Port: "critique"}},
		},
	}
	issues = ValidatePipelineBody(context.Background(), mismatch, cat)
	if len(issues) != 1 || issues[0].NodeID != "a" {
		t.Fatalf("issues = %+v", issues)
	}

	// Cycle among non-loop edges.
	cycle := PipelineBody{
		Nodes: []PipelineNode{
			{ID: "a", Type: "agent"}, {ID: "b", Type: "agent"}, {ID: "c", Type: "agent"},
		},
		Edges: []PipelineEdge{
			{ID: "1", From: EdgeEndpoint{Node: "a", Port: "html"}, To: EdgeEndpoint{Node: "b", Port: "html"}},
			{ID: "2", From: EdgeEndpoint{Node: "b", Port: "html"}, To: EdgeEndpoint{Node: "c", Port: "html"}},
			{ID: "3", From: EdgeEndpoint{Node: "c", Port: "html"}, To: EdgeEndpoint{Node: "a", Port: "html"}},
		},
	}
	issues = ValidatePipelineBody(context.Background(), cycle, cat)
	if len(issues) == 0 || !strings.Contains(issues[len(issues)-1].Message, "DAG") {
		t.Fatalf("cycle issues = %+v", issues)
	}

	// Agent reference warning.
	noAgent := PipelineBody{
		Nodes: []PipelineNode{{ID: "a", Type: "agent", Config: map[string]any{"agent_id": "ghost"}}},
	}
	issues = ValidatePipelineBody(context.Background(), noAgent, cat)
	if len(issues) != 1 || issues[0].Severity != "warning" {
		t.Fatalf("agent issues = %+v", issues)
	}

	// Missing agent_id config.
	noConfig := PipelineBody{Nodes: []PipelineNode{{ID: "a", Type: "agent"}}}
	issues = ValidatePipelineBody(context.Background(), noConfig, cat)
	if len(issues) != 1 || issues[0].Severity != "warning" {
		t.Fatalf("no-config issues = %+v", issues)
	}
}

/* ---------- export/import round-trip ---------- */

func TestExportImportRoundTrip(t *testing.T) {
	h := setup(t)
	if w := h.do(http.MethodPost, "/studio/model_set", `{"slug":"main","name":"Main","draft":`+marshal(t, okModelSet())+`}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body)
	}
	var list struct {
		Items []models.StudioDef `json:"items"`
	}
	_ = json.Unmarshal(h.do(http.MethodGet, "/studio/model_set", "").Body.Bytes(), &list)
	msID := list.Items[0].ID

	for kind, body := range map[string]any{
		"agent": okAgent(msID), "skill": okSkill, "rule": okRule, "pipeline": okPipeline,
	} {
		if w := h.do(http.MethodPost, "/studio/"+kind, `{"slug":"x-`+kind+`","name":"X","draft":`+marshal(t, body)+`}`); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", kind, w.Code, w.Body)
		}
	}
	// Publish the model_set so export uses the published body.
	if w := h.do(http.MethodPost, "/studio/model_set/"+msID+"/publish", `{"note":"v1"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}

	var bundle exportBundle
	h.get(t, "/studio/export", &bundle)
	if bundle.Version != 1 || len(bundle.Defs) != 5 {
		t.Fatalf("bundle = %+v", bundle)
	}
	for _, d := range bundle.Defs {
		if d.Kind == "model_set" && d.Version != 1 {
			t.Errorf("published def exported with version %d", d.Version)
		}
		if d.Kind == "agent" && d.Version != 0 {
			t.Errorf("draft-only def exported with version %d", d.Version)
		}
	}

	// Re-import into the same database: every def exists → publish as new
	// version + overwrite draft, no errors.
	res := h.do(http.MethodPost, "/studio/import", marshal(t, bundle))
	if res.Code != http.StatusOK {
		t.Fatalf("import = %d %s", res.Code, res.Body)
	}
	var out importResult
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Imported != 5 || out.Skipped != 0 || len(out.Errors) != 0 {
		t.Fatalf("import result = %+v", out)
	}
	var versions struct {
		Items []models.StudioDefVersion `json:"items"`
	}
	_ = json.Unmarshal(h.do(http.MethodGet, "/studio/model_set/"+msID+"/versions", "").Body.Bytes(), &versions)
	if len(versions.Items) != 2 || versions.Items[0].Note != "imported" {
		t.Fatalf("versions after import = %+v", versions.Items)
	}

	// Import self-contained defs into a fresh database (created + published).
	fresh := exportBundle{Version: 1, ExportedAt: bundle.ExportedAt}
	for _, d := range bundle.Defs {
		if d.Kind == "agent" || d.Kind == "pipeline" {
			continue // cross-references def ids from the source database
		}
		fresh.Defs = append(fresh.Defs, d)
	}
	h2 := setup(t)
	res = h2.do(http.MethodPost, "/studio/import", marshal(t, fresh))
	if res.Code != http.StatusOK {
		t.Fatalf("fresh import = %d %s", res.Code, res.Body)
	}
	var out2 importResult
	_ = json.Unmarshal(res.Body.Bytes(), &out2)
	if out2.Imported != 3 || len(out2.Errors) != 0 {
		t.Fatalf("fresh import result = %+v", out2)
	}
	// Skips and per-entry errors.
	broken := exportBundle{Version: 1, Defs: []exportDef{
		{Kind: "weird", Slug: "a", Body: json.RawMessage(`{}`)},
		{Kind: "skill", Slug: "", Body: json.RawMessage(`{}`)},
		{Kind: "rule", Slug: "bad", Body: json.RawMessage(`{"groups":[]}`)},
	}}
	res = h2.do(http.MethodPost, "/studio/import", marshal(t, broken))
	var out3 importResult
	_ = json.Unmarshal(res.Body.Bytes(), &out3)
	if out3.Skipped != 2 || len(out3.Errors) != 1 {
		t.Fatalf("broken import result = %+v", out3)
	}
}

/* ---------- catalog ---------- */

func TestCatalog(t *testing.T) {
	h := setup(t)
	var cat CatalogData
	h.get(t, "/studio/catalog", &cat)
	if len(cat.PortTypes) != 5 {
		t.Errorf("port types = %d", len(cat.PortTypes))
	}
	types := map[string]bool{}
	for _, nt := range cat.NodeTypes {
		types[nt.Type] = true
	}
	for _, want := range []string{"input", "agent", "validate", "render", "merge", "gate", "output", "pick_best"} {
		if !types[want] {
			t.Errorf("missing node type %q", want)
		}
	}
	if len(cat.Tools) != 14 {
		t.Errorf("tools = %d", len(cat.Tools))
	}
	for _, t2 := range cat.Tools {
		if len(t2.Description) == 0 || t2.Flags == nil {
			t.Errorf("tool %q missing description/flags", t2.Name)
		}
	}
	if len(cat.PromptSections) != 3 || len(cat.PromptVariables) < 8 {
		t.Errorf("sections=%d vars=%d", len(cat.PromptSections), len(cat.PromptVariables))
	}
	if len(cat.ValidatorRuleCodes) == 0 {
		t.Error("validator codes empty")
	}
	// render defaults
	render := cat.NodeType("render")
	if render == nil || render.Defaults["width"] != float64(1280) && render.Defaults["width"] != int(1280) {
		t.Errorf("render defaults = %v", render.Defaults)
	}
}

/* ---------- seeding ---------- */

func TestSeedDefaultsIdempotent(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	if err := SeedDefaults(ctx, h.store, log); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(ctx, h.store, log); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]int{KindModelSet: 1, KindAgent: 7, KindPipeline: 1, KindRule: 1, KindSkill: 0} {
		defs, err := h.store.List(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(defs) != want {
			t.Errorf("%s = %d defs, want %d", kind, len(defs), want)
		}
	}
	// The seeded pipeline validates against the catalog.
	def, ver, err := h.store.LatestPublished(ctx, KindPipeline, "default")
	if err != nil {
		t.Fatalf("seeded pipeline not published: %v", err)
	}
	_ = def
	var body PipelineBody
	if err := json.Unmarshal(ver.Body, &body); err != nil {
		t.Fatal(err)
	}
	cat := &CatalogData{NodeTypes: catalogNodeTypes()}
	// Agent references need agents in the catalog; collect from the store.
	agents, err := h.store.List(ctx, KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		cat.Agents = append(cat.Agents, StudioRef{ID: a.ID, Slug: a.Slug, Name: a.Name})
	}
	if issues := ValidatePipelineBody(ctx, body, cat); len(issues) > 0 {
		t.Fatalf("seeded pipeline issues: %+v", issues)
	}
}
