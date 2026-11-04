package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tools/fstools"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/vfstest"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

type harness struct {
	t      *testing.T
	s      *Store
	thread string
	run    string
}

func setup(t *testing.T) *harness {
	t.Helper()
	db, err := dbx.Open(filepath.Join(t.TempDir(), "canvas.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbx.Close(db) })
	if err := db.AutoMigrate(&models.Run{}, &models.Artifact{}, &blobs.Blob{}, &models.CanvasFile{}, &models.CanvasState{}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, s: NewStore(db, blobs.NewStore(db))}
	h.thread = h.newRun("", "user1")
	h.run = h.thread
	return h
}

// newRun inserts a run of the thread ("" = a new thread) and returns its id.
func (h *harness) newRun(thread, owner string) string {
	h.t.Helper()
	id := models.NewID()
	if thread == "" {
		thread = id
	}
	if err := h.s.db.Create(&models.Run{Base: models.Base{ID: id}, OwnerID: owner, ThreadID: thread, Status: models.RunCompleted}).Error; err != nil {
		h.t.Fatal(err)
	}
	return id
}

// artifact stores html as an artifact of the harness run.
func (h *harness) artifact(html string) string {
	h.t.Helper()
	sha, err := h.s.blobs.Put(context.Background(), []byte(html), "text/html; charset=utf-8")
	if err != nil {
		h.t.Fatal(err)
	}
	a := models.Artifact{Base: models.Base{ID: models.NewID()}, RunID: h.run, Kind: models.ArtifactHTML, BlobSHA: sha}
	if err := h.s.db.Create(&a).Error; err != nil {
		h.t.Fatal(err)
	}
	return a.ID
}

// seed inserts live files directly (no path rules).
func (h *harness) seed(files map[string]string) {
	h.t.Helper()
	for p, html := range files {
		f := models.CanvasFile{ThreadID: h.thread, Path: p, ArtifactID: h.artifact(html), RunID: h.run, Size: int64(len(html))}
		if err := h.s.db.Create(&f).Error; err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) commit(dest Dest, names ...string) (string, []string) {
	h.t.Helper()
	var items []CommitItem
	for _, n := range names {
		items = append(items, CommitItem{Name: n, ArtifactID: h.artifact("<div>" + n + "</div>")})
	}
	folder, files, err := h.s.Commit(context.Background(), h.thread, h.run, dest, items)
	if err != nil {
		h.t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return folder, paths
}

func (h *harness) livePaths() []string {
	h.t.Helper()
	files, err := h.s.Files(context.Background(), h.thread)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func (h *harness) notes() []string {
	h.t.Helper()
	st, err := h.s.State(context.Background(), h.thread)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, n := range st.Notes {
		out = append(out, n.Text)
	}
	return out
}

/* ---------- paths ---------- */

func TestPathRules(t *testing.T) {
	for in, want := range map[string]string{
		"Pricing Tiers":           "pricing-tiers",
		"  Crème brûlée!! ":       "creme-brulee",
		"---":                     "design",
		"Dark   theme / v2":       "dark-theme-v2",
		strings.Repeat("ab ", 30): strings.TrimSuffix(strings.Repeat("ab-", 16), "-"),
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"make it dark":                           "dark",
		"Give me 5 buttons":                      "5-buttons",
		"A pricing card with three tiers please": "pricing-card-three-tiers",
		"the":                                    "the",
	} {
		if got := BriefSlug(in); got != want {
			t.Errorf("BriefSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got, err := NormalizeFolder("Pricing/Dark Theme/"); err != nil || got != "/pricing/dark-theme" {
		t.Errorf("NormalizeFolder = %q, %v", got, err)
	}
	if got, err := NormalizeFolder(""); err != nil || got != "/" {
		t.Errorf("NormalizeFolder(empty) = %q, %v", got, err)
	}
	if _, err := NormalizeFolder("/a/b/c/d/e"); !errors.Is(err, ErrBadPath) {
		t.Errorf("depth 5: %v", err)
	}
	if got, err := NormalizeFile("/Pricing/Tiers Card.HTML"); err != nil || got != "/pricing/tiers-card.html" {
		t.Errorf("NormalizeFile = %q, %v", got, err)
	}
	for _, bad := range []string{"/a.txt", "/A/b.html", "/a/.x.html", "relative.html", "/a//../b.html", "/"} {
		if _, err := CleanFile(bad); err == nil {
			t.Errorf("CleanFile(%q) accepted", bad)
		}
	}
	if _, err := CleanFile("/a-1/b/tiers.html"); err != nil {
		t.Errorf("CleanFile: %v", err)
	}
	taken := map[string]bool{"/a/x.html": true, "/a/x-2.html": true, "/f": true}
	if got := uniqueFile("/a/x.html", func(p string) bool { return taken[p] }); got != "/a/x-3.html" {
		t.Errorf("uniqueFile = %s", got)
	}
	if got := UniqueFolder("/f", func(p string) bool { return taken[p] }); got != "/f-2" {
		t.Errorf("UniqueFolder = %s", got)
	}
	for in, want := range map[string]string{"Pricing/Dark": "/pricing-dark", "/cta/": "/cta", "dark theme": "/dark-theme", " / ": "", "": ""} {
		if got := TopFolder(in); got != want {
			t.Errorf("TopFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

/* ---------- commit ---------- */

func TestCommitMakesNamesAndFreshFoldersUnique(t *testing.T) {
	h := setup(t)
	folder, paths := h.commit(Dest{Folder: "/pricing", Fresh: true}, "tiers.html", "tiers.html")
	if folder != "/pricing" || !slices.Equal(paths, []string{"/pricing/tiers.html", "/pricing/tiers-2.html"}) {
		t.Fatalf("first commit: %s %v", folder, paths)
	}
	// A fresh destination never joins an existing folder…
	folder, paths = h.commit(Dest{Folder: "/pricing", Fresh: true}, "tiers.html")
	if folder != "/pricing-2" || !slices.Equal(paths, []string{"/pricing-2/tiers.html"}) {
		t.Errorf("fresh commit: %s %v", folder, paths)
	}
	// …an agent-chosen one may.
	folder, paths = h.commit(Dest{Folder: "/pricing"}, "Tiers")
	if folder != "/pricing" || !slices.Equal(paths, []string{"/pricing/tiers-3.html"}) {
		t.Errorf("join commit: %s %v", folder, paths)
	}
	files, _ := h.s.Files(context.Background(), h.thread)
	if files[0].Size == 0 || files[0].RunID != h.run {
		t.Errorf("row = %+v", files[0])
	}
	got, _ := h.s.PathsOfRun(context.Background(), h.run)
	if len(got) != 4 {
		t.Errorf("paths of run = %v", got)
	}
	// The partial unique index guards live paths.
	dup := models.CanvasFile{ThreadID: h.thread, Path: "/pricing/tiers.html", ArtifactID: files[0].ArtifactID}
	if err := h.s.db.Create(&dup).Error; err == nil {
		t.Error("a second live row with the same path was accepted")
	}
}

/* ---------- delete / restore ---------- */

func TestDeleteRestoreAndNotes(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.commit(Dest{Folder: "/hero"}, "a.html", "b.html")
	h.commit(Dest{Folder: "/cta"}, "c.html")

	op, deleted, err := h.s.Delete(ctx, h.thread, "user1", []string{"/hero", "/hero/a.html", "/cta/c.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []string{"/cta/c.html", "/hero/a.html", "/hero/b.html"}) {
		t.Errorf("deleted = %v", deleted)
	}
	if got := h.livePaths(); len(got) != 0 {
		t.Errorf("live after delete = %v", got)
	}
	notes := h.notes()
	if len(notes) != 1 || notes[0] != "deleted /hero/ (2 components: /hero/a.html, /hero/b.html); /cta/c.html" {
		t.Errorf("notes = %q", notes)
	}
	if _, _, err := h.s.Delete(ctx, h.thread, "user1", []string{"/nope"}); !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Errorf("delete unknown: %v", err)
	}

	// Undo while the note is pending: the note disappears.
	restored, err := h.s.Restore(ctx, h.thread, "user1", op)
	if err != nil || len(restored) != 3 {
		t.Fatalf("restore: %v %v", restored, err)
	}
	if got := h.notes(); len(got) != 0 {
		t.Errorf("notes after undo = %q", got)
	}
	if got := h.livePaths(); len(got) != 3 {
		t.Errorf("live after restore = %v", got)
	}

	// Delete again, deliver the note, then restore: a "restored" note follows.
	op, _, _ = h.s.Delete(ctx, h.thread, "user1", []string{"/cta"})
	taken, err := h.s.TakeNotes(ctx, h.thread)
	if err != nil || len(taken) != 1 || !strings.HasPrefix(taken[0].Text, "deleted /cta/") {
		t.Fatalf("take notes = %v %v", taken, err)
	}
	if again, _ := h.s.TakeNotes(ctx, h.thread); len(again) != 0 {
		t.Errorf("notes delivered twice: %v", again)
	}
	// Something new took the path meanwhile → 409.
	h.commit(Dest{Folder: "/cta"}, "c.html")
	if _, err := h.s.Restore(ctx, h.thread, "user1", op); !apperr.IsCode(err, apperr.CodeConflict) {
		t.Errorf("restore over a live path: %v", err)
	}
	h.s.Delete(ctx, h.thread, "user1", []string{"/cta/c.html"})
	h.s.TakeNotes(ctx, h.thread)
	if _, err := h.s.Restore(ctx, h.thread, "user1", op); err != nil {
		t.Fatal(err)
	}
	if got := h.notes(); len(got) != 1 || got[0] != "restored /cta/c.html" {
		t.Errorf("notes after late restore = %q", got)
	}
}

/* ---------- move ---------- */

func TestMoveFilesAndFolders(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.commit(Dest{Folder: "/buttons"}, "cta.html", "ghost.html")
	// A version of cta.html in a subfolder.
	if _, _, err := h.s.Commit(ctx, h.thread, h.run, Dest{Folder: "/buttons/dark"},
		[]CommitItem{{Name: "cta.html", ArtifactID: h.artifact("<b>dark</b>"), SourcePath: "/buttons/cta.html"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.PutLayout(ctx, h.thread, "user1", models.CanvasLayout{
		Pos:   map[string]models.Point{"g:/buttons": {X: 10, Y: 20}, "c:/buttons/cta.html": {X: 5}, "g:/buttons/dark": {Y: 3}},
		Scale: map[string]float64{"/buttons/cta.html": 2},
		Dir:   map[string]string{"/buttons": "row"},
	}); err != nil {
		t.Fatal(err)
	}

	moved, err := h.s.Move(ctx, h.thread, "user1", "/buttons/cta.html", "/CTA/Main.html")
	if err != nil || len(moved) != 1 || moved[0].To != "/cta/main.html" {
		t.Fatalf("move file: %v %v", moved, err)
	}
	files, _ := h.s.Files(ctx, h.thread)
	for _, f := range files {
		if f.Path == "/buttons/dark/cta.html" && f.SourcePath != "/cta/main.html" {
			t.Errorf("source path not remapped: %+v", f)
		}
	}

	moved, err = h.s.Move(ctx, h.thread, "user1", "/buttons", "/actions")
	if err != nil || len(moved) != 2 {
		t.Fatalf("move folder: %v %v", moved, err)
	}
	if got := h.livePaths(); !slices.Equal(got, []string{"/actions/dark/cta.html", "/actions/ghost.html", "/cta/main.html"}) {
		t.Errorf("paths = %v", got)
	}
	st, _ := h.s.State(ctx, h.thread)
	l := st.Layout
	if _, ok := l.Pos["g:/actions"]; !ok || l.Pos["c:/cta/main.html"].X != 5 || l.Scale["/cta/main.html"] != 2 || l.Dir["/actions"] != "row" {
		t.Errorf("layout not remapped: %+v", l)
	}
	if _, ok := l.Pos["g:/actions/dark"]; !ok {
		t.Errorf("nested group key not remapped: %+v", l.Pos)
	}
	if n := h.notes(); len(n) != 2 || n[0] != "moved /buttons/cta.html → /cta/main.html" || !strings.HasPrefix(n[1], "moved folder /buttons/ → /actions/") {
		t.Errorf("notes = %q", n)
	}

	for name, tc := range map[string][2]string{
		"file conflict":   {"/actions/ghost.html", "/cta/main.html"},
		"folder conflict": {"/actions", "/cta"},
	} {
		if _, err := h.s.Move(ctx, h.thread, "user1", tc[0], tc[1]); !apperr.IsCode(err, apperr.CodeConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.s.Move(ctx, h.thread, "user1", "/actions", "/actions/inner"); !apperr.IsCode(err, apperr.CodeInvalid) {
		t.Errorf("into itself: %v", err)
	}
	if _, err := h.s.Move(ctx, h.thread, "user1", "/nope.html", "/x.html"); !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

/* ---------- layout ---------- */

func TestPutLayoutValidates(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.commit(Dest{Folder: "/a/b"}, "x.html")
	out, err := h.s.PutLayout(ctx, h.thread, "user1", models.CanvasLayout{
		Pos: map[string]models.Point{
			"g:/a": {X: 1}, "g:/a/b": {X: 2}, "g:/": {X: 3}, "c:/a/b/x.html": {X: 4},
			"g:/stale": {X: 5}, "c:/a/b/stale.html": {X: 6}, "weird": {X: 7},
		},
		Scale: map[string]float64{"/a/b/x.html": 9, "/nope.html": 1},
		Dir:   map[string]string{"/a": "col", "/a/b": "diagonal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Pos) != 4 || out.Scale["/a/b/x.html"] != maxScale || len(out.Scale) != 1 || len(out.Dir) != 1 {
		t.Errorf("layout = %+v", out)
	}
	big := models.CanvasLayout{Pos: map[string]models.Point{}}
	for i := 0; i <= maxLayoutEntries; i++ {
		big.Pos["c:/"+models.NewID()] = models.Point{}
	}
	if _, err := h.s.PutLayout(ctx, h.thread, "user1", big); !apperr.IsCode(err, apperr.CodeInvalid) {
		t.Errorf("oversized layout: %v", err)
	}
	st, _ := h.s.State(ctx, h.thread)
	if st.OwnerID != "user1" || len(st.Layout.Pos) != 4 {
		t.Errorf("stored state = %+v", st)
	}
}

/* ---------- access + HTTP ---------- */

func TestAccessAndHTTP(t *testing.T) {
	h := setup(t)
	h.commit(Dest{Folder: "/pricing"}, "tiers.html")
	ctx := context.Background()
	if owner, err := h.s.Access(ctx, h.thread, Viewer{UserID: "user1"}); err != nil || owner != "user1" {
		t.Errorf("owner access: %q %v", owner, err)
	}
	if _, err := h.s.Access(ctx, h.thread, Viewer{UserID: "user2"}); !apperr.IsCode(err, apperr.CodeNotFound) {
		t.Errorf("other user: %v", err)
	}
	if _, err := h.s.Access(ctx, h.thread, Viewer{UserID: "admin", Admin: true}); err != nil {
		t.Errorf("admin: %v", err)
	}

	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", "canvas-test")
	token := func(id, role string) string {
		tok, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: id}, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + tok
	}
	r := chi.NewRouter()
	NewController(h.s, mw, nil).Routes(r)
	do := func(method, path, body, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", tok)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	user, other := token("user1", "user"), token("user2", "user")
	base := "/threads/" + h.thread + "/canvas"

	w := do("GET", base, "", user)
	if w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	var view map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	files, _ := view["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "/pricing/tiers.html" ||
		!strings.Contains(w.Body.String(), `"pending_notes":[]`) || !strings.Contains(w.Body.String(), `"pos":{}`) {
		t.Errorf("view = %s", w.Body)
	}
	if w := do("GET", base, "", other); w.Code != 404 {
		t.Errorf("other user get: %d", w.Code)
	}
	if w := do("GET", "/threads/nope/canvas", "", user); w.Code != 404 {
		t.Errorf("unknown thread: %d", w.Code)
	}
	if w := do("PUT", base+"/layout", `{"layout":{"pos":{"g:/pricing":{"x":1,"y":2}}}}`, user); w.Code != 200 || !strings.Contains(w.Body.String(), `"g:/pricing":{"x":1,"y":2}`) {
		t.Errorf("put layout: %d %s", w.Code, w.Body)
	}
	if w := do("POST", base+"/move", `{"from":"/pricing","to":"Plans"}`, user); w.Code != 200 || !strings.Contains(w.Body.String(), `"to":"/plans/tiers.html"`) {
		t.Errorf("move: %d %s", w.Code, w.Body)
	}
	w = do("POST", base+"/delete", `{"paths":["/plans"]}`, user)
	var del struct {
		OpID    string   `json:"op_id"`
		Deleted []string `json:"deleted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &del)
	if w.Code != 200 || del.OpID == "" || len(del.Deleted) != 1 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := do("POST", base+"/delete", `{"paths":["/plans"]}`, other); w.Code != 404 {
		t.Errorf("other user delete: %d", w.Code)
	}
	if w := do("POST", base+"/restore", `{"op_id":"`+del.OpID+`"}`, user); w.Code != 200 || !strings.Contains(w.Body.String(), "/plans/tiers.html") {
		t.Errorf("restore: %d %s", w.Code, w.Body)
	}
	if w := do("POST", base+"/delete", `{"paths":[]}`, user); w.Code != http.StatusBadRequest {
		t.Errorf("empty delete: %d", w.Code)
	}
}

/* ---------- FS ---------- */

func TestFSConformance(t *testing.T) {
	vfstest.RunReadOnly(t, func(t *testing.T, files map[string][]byte) vfs.FS {
		h := setup(t)
		seed := map[string]string{}
		for p, b := range files {
			seed[p] = string(b)
		}
		h.seed(seed)
		return h.s.FS(h.thread)
	})
}

func TestFSHidesDeletedFilesAndWorksWithFileTools(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.commit(Dest{Folder: "/pricing"}, "tiers.html")
	h.commit(Dest{Folder: "/pricing/dark"}, "tiers.html")
	h.commit(Dest{Folder: "/hero"}, "a.html")
	if _, _, err := h.s.Delete(ctx, h.thread, "user1", []string{"/hero"}); err != nil {
		t.Fatal(err)
	}
	fsys := h.s.FS(h.thread)
	if _, err := fsys.Stat(ctx, "/hero"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("deleted folder: %v", err)
	}
	if fi, err := fsys.Stat(ctx, "/pricing/dark"); err != nil || !fi.IsDir {
		t.Errorf("implicit dir: %+v %v", fi, err)
	}
	infos, err := fsys.List(ctx, "/", false)
	if err != nil || len(infos) != 1 || infos[0].Path != "/pricing" || !infos[0].IsDir {
		t.Errorf("root list = %+v %v", infos, err)
	}
	// A different thread sees nothing.
	if _, err := h.s.FS("other").Stat(ctx, "/pricing"); !errors.Is(err, vfs.ErrNotFound) {
		t.Errorf("other thread: %v", err)
	}

	tools := fstools.New(fsys, fstools.Options{ReadOnly: true})
	call := func(name, input string) string {
		for _, tl := range tools {
			if tl.Spec().Name == name {
				out, err := tl.Call(ctx, json.RawMessage(input))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return out
			}
		}
		t.Fatalf("no tool %s", name)
		return ""
	}
	if out := call("glob", `{"pattern":"/**/*.html"}`); !strings.Contains(out, "/pricing/dark/tiers.html") || strings.Contains(out, "/hero") {
		t.Errorf("glob = %s", out)
	}
	if out := call("read_file", `{"path":"/pricing/tiers.html"}`); !strings.Contains(out, "<div>tiers.html</div>") {
		t.Errorf("read_file = %s", out)
	}
}
