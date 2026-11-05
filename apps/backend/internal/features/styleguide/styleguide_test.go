package styleguide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// fakeRunner answers every generation with text (or err) and records requests.
type fakeRunner struct {
	mu   sync.Mutex
	reqs []agentrun.NodeRequest
	text string
	err  error
}

func (f *fakeRunner) RunNode(_ context.Context, req agentrun.NodeRequest) (*agentrun.NodeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	return &agentrun.NodeResult{Text: f.text}, nil
}

type harness struct {
	t      *testing.T
	svc    *Service
	runner *fakeRunner
	router chi.Router
	tokens map[string]string
	agent  string
}

func setup(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	db, err := dbx.Open(filepath.Join(t.TempDir(), "sg.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.Styleguide{}, &blobs.Blob{}, &models.StudioDef{}, &models.StudioDefVersion{}); err != nil {
		t.Fatal(err)
	}
	defs := studio.NewStore(db)
	def, err := defs.Create(context.Background(), studio.KindAgent, studio.DefIdentity{Slug: studio.StyleguideAgentSlug, Name: "g"}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{text: "```markdown\n# Acme style guide\n\n## Colors\n\n#ff5500\n```"}
	svc := NewService(db, blobs.NewStore(db), defs, runner, log)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", "test")
	r := chi.NewRouter()
	NewController(svc, mw).Routes(r)
	h := &harness{t: t, svc: svc, runner: runner, router: r, tokens: map[string]string{}, agent: def.ID}
	for _, u := range []struct{ id, role string }{{"user1", "user"}, {"user2", "user"}, {"admin1", "admin"}} {
		tok, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: u.id}, Role: u.role})
		if err != nil {
			t.Fatal(err)
		}
		h.tokens[u.id] = "Bearer " + tok
	}
	return h
}

// wait blocks until background generations are done.
func (h *harness) wait() { h.svc.wg.Wait() }

var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func (h *harness) upload(user string, files map[string][]byte, fields map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("images", name)
		_, _ = fw.Write(data)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/styleguides", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", h.tokens[user])
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *harness) do(user, method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", h.tokens[user])
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func TestUploadGeneratesAGuide(t *testing.T) {
	h := setup(t)
	rec := h.upload("user1", map[string][]byte{"a.png": png, "b.png": png}, map[string]string{"notes": "use the orange"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	v := decode[View](t, rec)
	if v.Status != models.StyleguideGenerating || v.ImageCount != 2 || v.Notes != "use the orange" {
		t.Fatalf("created = %+v", v)
	}
	h.wait()

	got := decode[View](t, h.do("user1", http.MethodGet, "/styleguides/"+v.ID, ""))
	if got.Status != models.StyleguideReady || got.Name != "Acme" || !strings.HasPrefix(got.Markdown, "# Acme style guide") || strings.Contains(got.Markdown, "```") {
		t.Fatalf("generated = %+v", got)
	}
	// The generator got the images (as blob refs), the notes and the owner.
	req := h.runner.reqs[0]
	imgs, _ := req.Samples["images"].([]string)
	if req.AgentID != h.agent || len(imgs) != 2 || !strings.HasPrefix(imgs[0], "wcblob://") || req.Samples["notes"] != "use the orange" || req.Owner != "user1" {
		t.Errorf("generator request = %+v", req)
	}

	// Images are served back; other users see nothing.
	if rec := h.do("user1", http.MethodGet, "/styleguides/"+v.ID+"/images/1", ""); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("image = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := h.do("user1", http.MethodGet, "/styleguides/"+v.ID+"/images/2", ""); rec.Code != 404 {
		t.Errorf("image out of range = %d", rec.Code)
	}
	if rec := h.do("user2", http.MethodGet, "/styleguides/"+v.ID, ""); rec.Code != 404 {
		t.Errorf("other user get = %d", rec.Code)
	}
	if rec := h.do("admin1", http.MethodGet, "/styleguides/"+v.ID, ""); rec.Code != 200 {
		t.Errorf("admin get = %d", rec.Code)
	}
	list := decode[struct{ Items []View }](t, h.do("user2", http.MethodGet, "/styleguides", ""))
	if len(list.Items) != 0 {
		t.Errorf("other user list = %+v", list.Items)
	}
	list = decode[struct{ Items []View }](t, h.do("user1", http.MethodGet, "/styleguides", ""))
	if len(list.Items) != 1 || list.Items[0].ID != v.ID {
		t.Errorf("list = %+v", list.Items)
	}
}

func TestUploadValidation(t *testing.T) {
	h := setup(t)
	cases := map[string]struct {
		files  map[string][]byte
		fields map[string]string
	}{
		"no images":  {map[string][]byte{}, nil},
		"not image":  {map[string][]byte{"a.txt": []byte("hello")}, nil},
		"too many":   {map[string][]byte{"1.png": png, "2.png": png, "3.png": png, "4.png": png, "5.png": png}, nil},
		"long notes": {map[string][]byte{"a.png": png}, map[string]string{"notes": strings.Repeat("x", MaxNotesLen+1)}},
	}
	for name, c := range cases {
		if rec := h.upload("user1", c.files, c.fields); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	if len(h.runner.reqs) != 0 {
		t.Errorf("generations = %d", len(h.runner.reqs))
	}
}

func TestFailedGenerationCanBeRetriedOrWrittenByHand(t *testing.T) {
	h := setup(t)
	h.runner.err = errors.New("model exploded")
	v := decode[View](t, h.upload("user1", map[string][]byte{"a.png": png}, map[string]string{"name": "Mine"}))
	h.wait()
	got := decode[View](t, h.do("user1", http.MethodGet, "/styleguides/"+v.ID, ""))
	if got.Status != models.StyleguideFailed || got.Error != "model exploded" {
		t.Fatalf("failed = %+v", got)
	}

	h.runner.err = nil
	if rec := h.do("user1", http.MethodPost, "/styleguides/"+v.ID+"/regenerate", ""); rec.Code != 200 {
		t.Fatalf("regenerate = %d %s", rec.Code, rec.Body)
	}
	h.wait()
	got = decode[View](t, h.do("user1", http.MethodGet, "/styleguides/"+v.ID, ""))
	if got.Status != models.StyleguideReady || got.Name != "Mine" { // the user's name wins over the heading
		t.Fatalf("regenerated = %+v", got)
	}

	rec := h.do("user1", http.MethodPatch, "/styleguides/"+v.ID, `{"name":"Renamed","markdown":"# Hand written"}`)
	got = decode[View](t, rec)
	if rec.Code != 200 || got.Name != "Renamed" || got.Markdown != "# Hand written" {
		t.Fatalf("patch = %d %+v", rec.Code, got)
	}
	if rec := h.do("user1", http.MethodPatch, "/styleguides/"+v.ID, `{"name":"  "}`); rec.Code != 400 {
		t.Errorf("empty name = %d", rec.Code)
	}
	if rec := h.do("user2", http.MethodDelete, "/styleguides/"+v.ID, ""); rec.Code != 404 {
		t.Errorf("other user delete = %d", rec.Code)
	}
	if rec := h.do("user1", http.MethodDelete, "/styleguides/"+v.ID, ""); rec.Code != 204 {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec := h.do("user1", http.MethodGet, "/styleguides/"+v.ID, ""); rec.Code != 404 {
		t.Errorf("get deleted = %d", rec.Code)
	}
}

func TestRecoverMarksUnfinishedFailed(t *testing.T) {
	h := setup(t)
	sg := models.Styleguide{OwnerID: "user1", Status: models.StyleguideGenerating}
	if err := h.svc.db.Create(&sg).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := h.svc.Recover(context.Background()); err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	got, _ := h.svc.Get(context.Background(), Viewer{UserID: "user1"}, sg.ID)
	if got.Status != models.StyleguideFailed || got.Error == "" {
		t.Errorf("recovered = %+v", got)
	}
}

func TestNameAndMarkdownCleanup(t *testing.T) {
	for in, want := range map[string]string{
		"# Acme style guide\n\ntext": "Acme",
		"intro\n# Nordic Design System": "Nordic",
		"# Style guide":                "Style guide",
		"no heading":                   defaultName,
	} {
		if got := NameOf(in); got != want {
			t.Errorf("NameOf(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CleanMarkdown("```md\n# A\n\nb\n```"); got != "# A\n\nb" {
		t.Errorf("fence = %q", got)
	}
	if got := CleanMarkdown("# A\n\n```css\n:root{}\n```"); !strings.HasSuffix(got, "```") {
		t.Errorf("inner fence lost: %q", got)
	}
}
