// Package canvas is the shared state between the user and the agents of a
// chat thread: a tree of HTML component files (folders = groups) over
// immutable html artifacts, plus the per-thread view layout and the notes
// about user operations that the next run is told about.
//
// It must not import engine or agentrun (both depend on it).
package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
)

// Layout caps.
const (
	maxLayoutBytes   = 256 << 10
	maxLayoutEntries = 2000
	minScale         = 0.25
	maxScale         = 4
	// noteFiles is how many file paths a note lists.
	noteFiles = 5
)

// Viewer is the caller: a thread's canvas is visible to the owner of the
// thread's runs and to admins.
type Viewer struct {
	UserID string
	Admin  bool
}

// Store reads and changes canvases.
type Store struct {
	db    *gorm.DB
	blobs *blobs.Store
	now   func() time.Time
}

// NewStore builds the store.
func NewStore(db *gorm.DB, bl *blobs.Store) *Store {
	return &Store{db: db, blobs: bl, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) conn(ctx context.Context) *gorm.DB { return dbx.Conn(ctx, s.db) }

func live(q *gorm.DB) *gorm.DB { return q.Where("deleted_at IS NULL") }

/* ---------- access ---------- */

// Access checks that the viewer may see the thread and returns its owner
// (404 when the thread has no runs or belongs to someone else).
func (s *Store) Access(ctx context.Context, thread string, v Viewer) (string, error) {
	if thread == "" {
		return "", apperr.NotFound("chat thread not found")
	}
	var run models.Run
	err := s.conn(ctx).Select("owner_id").Where("thread_id = ?", thread).Order("created_at ASC, id ASC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", apperr.NotFound("chat thread not found")
	}
	if err != nil {
		return "", apperr.Internal(err)
	}
	if !v.Admin && run.OwnerID != v.UserID {
		return "", apperr.NotFound("chat thread not found")
	}
	return run.OwnerID, nil
}

/* ---------- reads ---------- */

// Files lists the live files of a thread, sorted by path.
func (s *Store) Files(ctx context.Context, thread string) ([]models.CanvasFile, error) {
	var out []models.CanvasFile
	if err := live(s.conn(ctx)).Where("thread_id = ?", thread).Order("path ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("canvas: files: %w", err)
	}
	return out, nil
}

// PathsOfRun lists the live paths committed by a run, in commit order.
func (s *Store) PathsOfRun(ctx context.Context, runID string) ([]string, error) {
	var out []string
	err := live(s.conn(ctx).Model(&models.CanvasFile{})).Where("run_id = ?", runID).
		Order("created_at ASC, id ASC").Pluck("path", &out).Error
	if err != nil {
		return nil, fmt.Errorf("canvas: run paths: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// Content returns the HTML of a file's artifact.
func (s *Store) Content(ctx context.Context, f models.CanvasFile) ([]byte, error) {
	var art models.Artifact
	if err := s.conn(ctx).Select("blob_sha").First(&art, "id = ?", f.ArtifactID).Error; err != nil {
		return nil, fmt.Errorf("canvas: artifact of %s: %w", f.Path, err)
	}
	b, err := s.blobs.Get(ctx, art.BlobSHA)
	if err != nil {
		return nil, err
	}
	return b.Data, nil
}

// Resolve expands selected paths (files or folders, recursively) into live
// files of the thread: deduplicated, in request order (folder contents by
// path). Unknown paths are a field error on `field`.
func (s *Store) Resolve(ctx context.Context, thread string, paths []string, field string) ([]models.CanvasFile, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	files, err := s.Files(ctx, thread)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	seen := map[string]bool{}
	var out []models.CanvasFile
	for _, raw := range paths {
		p, err := cleanAny(raw)
		if err != nil {
			return nil, apperr.Invalid("invalid input", map[string]string{field: err.Error()})
		}
		found := false
		for _, f := range files {
			if f.Path == p || (p == "/" || strings.HasPrefix(f.Path, p+"/")) {
				found = true
				if !seen[f.Path] {
					seen[f.Path] = true
					out = append(out, f)
				}
			}
		}
		if !found {
			return nil, apperr.Invalid("invalid input", map[string]string{field: p + " is not on the canvas"})
		}
	}
	return out, nil
}

// cleanAny validates a file or folder path (paths ending in .html are files).
func cleanAny(p string) (string, error) {
	if strings.HasSuffix(p, fileExt) {
		return CleanFile(p)
	}
	return CleanFolder(p)
}

/* ---------- commit (engine) ---------- */

// CommitItem is one result file of a run.
type CommitItem struct {
	Name       string // file name ("tiers.html"); normalized
	ArtifactID string
	SourcePath string
	Score      *float64
}

// Dest is where a run's results go.
type Dest struct {
	Folder string // normalized folder path
	// Fresh makes the folder unique (a taken folder gets "-2", …); otherwise
	// files join an existing folder and only their names are made unique.
	Fresh bool
}

// Commit inserts a run's results as new files. It runs on the transaction
// carried by ctx (dbx.Tx) when there is one. It returns the folder used and
// the created rows.
func (s *Store) Commit(ctx context.Context, thread, runID string, dest Dest, items []CommitItem) (string, []models.CanvasFile, error) {
	if len(items) == 0 {
		return dest.Folder, nil, nil
	}
	folder, err := CleanFolder(dest.Folder)
	if err != nil {
		return "", nil, err
	}
	db := s.conn(ctx)
	var paths []string
	if err := live(db.Model(&models.CanvasFile{})).Where("thread_id = ?", thread).Pluck("path", &paths).Error; err != nil {
		return "", nil, fmt.Errorf("canvas: commit: %w", err)
	}
	taken := map[string]bool{}
	folders := map[string]bool{}
	for _, p := range paths {
		taken[p] = true
		for _, d := range parents(p) {
			folders[d] = true
		}
	}
	if dest.Fresh {
		folder = uniqueFolder(folder, func(f string) bool { return folders[f] })
	}
	out := make([]models.CanvasFile, 0, len(items))
	for _, it := range items {
		var art struct {
			BlobSHA string
		}
		if err := db.Model(&models.Artifact{}).Select("blob_sha").Where("id = ?", it.ArtifactID).Take(&art).Error; err != nil {
			return "", nil, fmt.Errorf("canvas: commit artifact %s: %w", it.ArtifactID, err)
		}
		var size int64
		db.Model(&blobs.Blob{}).Select("size").Where("sha256 = ?", art.BlobSHA).Scan(&size)
		p := uniqueFile(Join(folder, NormalizeName(it.Name)), func(p string) bool { return taken[p] })
		taken[p] = true
		f := models.CanvasFile{
			ThreadID: thread, Path: p, ArtifactID: it.ArtifactID, RunID: runID,
			SourcePath: it.SourcePath, Score: it.Score, Size: size,
		}
		if err := db.Create(&f).Error; err != nil {
			return "", nil, fmt.Errorf("canvas: commit %s: %w", p, err)
		}
		out = append(out, f)
	}
	return folder, out, nil
}

// parents lists the folders containing p ("/" included).
func parents(p string) []string {
	out := []string{"/"}
	for i := 1; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

/* ---------- state + notes ---------- */

// State returns the thread's canvas state (zero value when none was stored).
func (s *Store) State(ctx context.Context, thread string) (*models.CanvasState, error) {
	var st models.CanvasState
	err := s.conn(ctx).First(&st, "thread_id = ?", thread).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &models.CanvasState{ThreadID: thread}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("canvas: state: %w", err)
	}
	return &st, nil
}

func (s *Store) saveState(ctx context.Context, st *models.CanvasState) error {
	st.UpdatedAt = s.now()
	if err := s.conn(ctx).Save(st).Error; err != nil {
		return fmt.Errorf("canvas: save state: %w", err)
	}
	return nil
}

// TakeNotes returns the pending notes of a thread and clears them (a run
// receives each note once). Call it inside the run-creation transaction.
func (s *Store) TakeNotes(ctx context.Context, thread string) ([]models.CanvasNote, error) {
	st, err := s.State(ctx, thread)
	if err != nil || len(st.Notes) == 0 {
		return nil, err
	}
	notes := st.Notes
	st.Notes = []models.CanvasNote{}
	if err := s.saveState(ctx, st); err != nil {
		return nil, err
	}
	return notes, nil
}

// mutate loads the thread state, applies fn and saves it, inside ctx's tx.
func (s *Store) mutate(ctx context.Context, thread, owner string, fn func(st *models.CanvasState)) error {
	st, err := s.State(ctx, thread)
	if err != nil {
		return err
	}
	if owner != "" {
		st.OwnerID = owner
	}
	fn(st)
	return s.saveState(ctx, st)
}

func (s *Store) note(id, text string) models.CanvasNote {
	return models.CanvasNote{ID: id, Text: text, At: s.now()}
}

/* ---------- user operations ---------- */

// Delete soft-deletes files and folders (a folder = every live file below it)
// as one operation and records a note for the next run.
func (s *Store) Delete(ctx context.Context, thread, owner string, paths []string) (string, []string, error) {
	if len(paths) == 0 {
		return "", nil, apperr.Invalid("nothing to delete", map[string]string{"paths": "is required"})
	}
	op := models.NewID()
	var deleted []string
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		files, err := s.Files(ctx, thread)
		if err != nil {
			return err
		}
		var parts []string
		gone := map[string]bool{}
		for _, raw := range paths {
			p, err := cleanAny(raw)
			if err != nil {
				return apperr.Invalid("invalid path", map[string]string{"paths": err.Error()})
			}
			var hit []string
			for _, f := range files {
				if (f.Path == p || within(f.Path, p)) && !gone[f.Path] {
					gone[f.Path] = true
					hit = append(hit, f.Path)
				}
			}
			if len(hit) == 0 {
				if anyMatch(files, p) {
					continue // already covered by an earlier path of this request
				}
				return apperr.NotFound(p + " is not on the canvas")
			}
			deleted = append(deleted, hit...)
			parts = append(parts, describe(p, hit))
		}
		now := s.now()
		if err := s.conn(ctx).Model(&models.CanvasFile{}).
			Where("thread_id = ? AND deleted_at IS NULL AND path IN ?", thread, deleted).
			Updates(map[string]any{"deleted_at": now, "delete_op": op, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("canvas: delete: %w", err)
		}
		return s.mutate(ctx, thread, owner, func(st *models.CanvasState) {
			st.Notes = append(st.Notes, s.note(op, "deleted "+strings.Join(parts, "; ")))
		})
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(deleted)
	return op, deleted, nil
}

func anyMatch(files []models.CanvasFile, p string) bool {
	for _, f := range files {
		if f.Path == p || within(f.Path, p) {
			return true
		}
	}
	return false
}

// describe renders "/hero/a.html" or "/hero/ (2 components: /hero/a.html, …)".
func describe(p string, files []string) string {
	if len(files) == 1 && files[0] == p {
		return p
	}
	label := p
	if label != "/" {
		label += "/"
	}
	list := files
	more := ""
	if len(list) > noteFiles {
		more = fmt.Sprintf(", … %d more", len(list)-noteFiles)
		list = list[:noteFiles]
	}
	noun := "components"
	if len(files) == 1 {
		noun = "component"
	}
	return fmt.Sprintf("%s (%d %s: %s%s)", label, len(files), noun, strings.Join(list, ", "), more)
}

// Restore undoes a delete operation. A path that is live again → 409.
func (s *Store) Restore(ctx context.Context, thread, owner, op string) ([]string, error) {
	if op == "" {
		return nil, apperr.Invalid("invalid input", map[string]string{"op_id": "is required"})
	}
	var restored []string
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		var rows []models.CanvasFile
		if err := s.conn(ctx).Where("thread_id = ? AND delete_op = ? AND deleted_at IS NOT NULL", thread, op).
			Order("path ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("canvas: restore: %w", err)
		}
		if len(rows) == 0 {
			return apperr.NotFound("nothing to restore")
		}
		for _, r := range rows {
			restored = append(restored, r.Path)
		}
		var clash []string
		if err := live(s.conn(ctx).Model(&models.CanvasFile{})).
			Where("thread_id = ? AND path IN ?", thread, restored).Pluck("path", &clash).Error; err != nil {
			return fmt.Errorf("canvas: restore: %w", err)
		}
		if len(clash) > 0 {
			return apperr.Conflict("cannot restore: " + strings.Join(clash, ", ") + " exists again")
		}
		if err := s.conn(ctx).Model(&models.CanvasFile{}).Where("thread_id = ? AND delete_op = ?", thread, op).
			Updates(map[string]any{"deleted_at": nil, "delete_op": "", "updated_at": s.now()}).Error; err != nil {
			return fmt.Errorf("canvas: restore: %w", err)
		}
		return s.mutate(ctx, thread, owner, func(st *models.CanvasState) {
			for i, n := range st.Notes {
				if n.ID == op {
					st.Notes = append(st.Notes[:i:i], st.Notes[i+1:]...)
					return
				}
			}
			st.Notes = append(st.Notes, s.note(models.NewID(), "restored "+describeList(restored)))
		})
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

func describeList(paths []string) string {
	if len(paths) <= noteFiles {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:noteFiles], ", ") + fmt.Sprintf(", … %d more", len(paths)-noteFiles)
}

// Moved is one path change of a move.
type Moved struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Move renames/moves a file (file → file) or a folder (folder → folder). The
// target is normalized (slugged segments). Layout keys follow the move.
func (s *Store) Move(ctx context.Context, thread, owner, from, to string) ([]Moved, error) {
	isFile := strings.HasSuffix(from, fileExt)
	var err error
	if from, err = cleanAny(from); err != nil {
		return nil, apperr.Invalid("invalid path", map[string]string{"from": err.Error()})
	}
	if isFile {
		to, err = NormalizeFile(to)
	} else {
		to, err = NormalizeFolder(to)
	}
	if err != nil {
		return nil, apperr.Invalid("invalid path", map[string]string{"to": err.Error()})
	}
	if from == "/" {
		return nil, apperr.Invalid("invalid path", map[string]string{"from": "the root folder cannot be moved"})
	}
	if from == to {
		return []Moved{}, nil
	}
	if !isFile && within(to, from) {
		return nil, apperr.Invalid("invalid path", map[string]string{"to": "a folder cannot move into itself"})
	}
	var moved []Moved
	err = dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		files, err := s.Files(ctx, thread)
		if err != nil {
			return err
		}
		remap := func(p string) (string, bool) {
			if isFile {
				return to, p == from
			}
			if within(p, from) {
				return to + p[len(from):], true
			}
			return p, false
		}
		livePaths := map[string]bool{}
		for _, f := range files {
			livePaths[f.Path] = true
		}
		for _, f := range files {
			np, ok := remap(f.Path)
			if !ok {
				continue
			}
			if livePaths[np] {
				return apperr.Conflict(np + " already exists")
			}
			if Depth(dirOf(np)) > MaxDepth {
				return apperr.Invalid("invalid path", map[string]string{"to": fmt.Sprintf("folders nest at most %d levels", MaxDepth)})
			}
			moved = append(moved, Moved{From: f.Path, To: np})
		}
		if len(moved) == 0 {
			return apperr.NotFound(from + " is not on the canvas")
		}
		if !isFile {
			for _, f := range files {
				if _, ok := remap(f.Path); !ok && within(f.Path, to) {
					return apperr.Conflict("folder " + to + " already exists")
				}
			}
		}
		db := s.conn(ctx)
		for _, m := range moved {
			if err := db.Model(&models.CanvasFile{}).Where("thread_id = ? AND path = ? AND deleted_at IS NULL", thread, m.From).
				Updates(map[string]any{"path": m.To, "updated_at": s.now()}).Error; err != nil {
				return fmt.Errorf("canvas: move: %w", err)
			}
			if err := db.Model(&models.CanvasFile{}).Where("thread_id = ? AND source_path = ?", thread, m.From).
				Update("source_path", m.To).Error; err != nil {
				return fmt.Errorf("canvas: move: %w", err)
			}
		}
		text := fmt.Sprintf("moved %s → %s", from, to)
		if !isFile {
			text = fmt.Sprintf("moved folder %s/ → %s/ (%d components)", from, to, len(moved))
		}
		return s.mutate(ctx, thread, owner, func(st *models.CanvasState) {
			st.Layout = remapLayout(st.Layout, remap)
			st.Notes = append(st.Notes, s.note(models.NewID(), text))
		})
	})
	if err != nil {
		return nil, err
	}
	return moved, nil
}

func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// remapLayout rewrites layout keys after a move.
func remapLayout(l models.CanvasLayout, remap func(string) (string, bool)) models.CanvasLayout {
	key := func(k string) string {
		prefix, p, ok := strings.Cut(k, ":")
		if !ok || (prefix != "c" && prefix != "g") {
			return k
		}
		if np, ok := remap(p); ok {
			return prefix + ":" + np
		}
		return k
	}
	path := func(p string) string {
		if np, ok := remap(p); ok {
			return np
		}
		return p
	}
	out := models.CanvasLayout{}
	if l.Pos != nil {
		out.Pos = make(map[string]models.Point, len(l.Pos))
		for k, v := range l.Pos {
			out.Pos[key(k)] = v
		}
	}
	if l.Scale != nil {
		out.Scale = make(map[string]float64, len(l.Scale))
		for k, v := range l.Scale {
			out.Scale[path(k)] = v
		}
	}
	if l.Dir != nil {
		out.Dir = make(map[string]string, len(l.Dir))
		for k, v := range l.Dir {
			out.Dir[path(k)] = v
		}
	}
	return out
}

/* ---------- layout ---------- */

// PutLayout replaces the thread's layout (last write wins). Keys must name
// files/folders of the thread (live or deleted); stale keys are dropped.
func (s *Store) PutLayout(ctx context.Context, thread, owner string, l models.CanvasLayout) (models.CanvasLayout, error) {
	if raw, _ := json.Marshal(l); len(raw) > maxLayoutBytes {
		return l, apperr.Invalid("the layout is too large", map[string]string{"layout": fmt.Sprintf("at most %d KB", maxLayoutBytes>>10)})
	}
	if len(l.Pos) > maxLayoutEntries || len(l.Scale) > maxLayoutEntries || len(l.Dir) > maxLayoutEntries {
		return l, apperr.Invalid("the layout is too large", map[string]string{"layout": fmt.Sprintf("at most %d entries per map", maxLayoutEntries)})
	}
	var out models.CanvasLayout
	err := dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		var paths []string
		if err := s.conn(ctx).Model(&models.CanvasFile{}).Where("thread_id = ?", thread).Distinct().Pluck("path", &paths).Error; err != nil {
			return fmt.Errorf("canvas: layout: %w", err)
		}
		files, folders := map[string]bool{}, map[string]bool{"/": true}
		for _, p := range paths {
			files[p] = true
			for _, d := range parents(p) {
				folders[d] = true
			}
		}
		out = models.CanvasLayout{Pos: map[string]models.Point{}, Scale: map[string]float64{}, Dir: map[string]string{}}
		for k, v := range l.Pos {
			prefix, p, _ := strings.Cut(k, ":")
			if !finite(v.X) || !finite(v.Y) {
				continue
			}
			if (prefix == "c" && files[p]) || (prefix == "g" && folders[p]) {
				out.Pos[k] = v
			}
		}
		for k, v := range l.Scale {
			if files[k] && finite(v) {
				out.Scale[k] = math.Min(maxScale, math.Max(minScale, v))
			}
		}
		for k, v := range l.Dir {
			if folders[k] && (v == "row" || v == "col") {
				out.Dir[k] = v
			}
		}
		return s.mutate(ctx, thread, owner, func(st *models.CanvasState) { st.Layout = out })
	})
	return out, err
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
