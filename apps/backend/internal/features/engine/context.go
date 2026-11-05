package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

const (
	// maxContextFiles caps context_paths per run (after folder expansion).
	maxContextFiles = 12
	// maxTreeLines caps the file tree shown to the agents.
	maxTreeLines = 200
	// maxLatestFiles is how many files of the latest result are shown.
	maxLatestFiles = 3
)

// canvasRun is what a run knows about its thread's canvas.
type canvasRun struct {
	seed     *portVal            // iteration-1 loop value (nil = nothing to say)
	selected []models.CanvasFile // context_paths, expanded
	brief    string
	history  string // agent mode: the chat so far (the `history` prompt variable)
}

// text is the canvas context for the `canvas` prompt variable.
func (c *canvasRun) text() string {
	if c == nil || c.seed == nil {
		return ""
	}
	return c.seed.Text
}

// resolveCanvas validates context_paths and computes the default destination
// folder of a run (§4.4 of the canvas plan).
func (e *Engine) resolveCanvas(ctx context.Context, p StartParams, threadID, brief string) (*canvasRun, string, error) {
	cr := &canvasRun{brief: brief}
	if e.Canvas == nil {
		return cr, "", nil
	}
	if len(p.ContextPaths) > 0 {
		if p.ThreadID == "" {
			return nil, "", apperr.Invalid("invalid input", map[string]string{"context_paths": "requires thread_id"})
		}
		if len(p.ContextPaths) > maxContextFiles {
			return nil, "", apperr.Invalid("invalid input", map[string]string{"context_paths": fmt.Sprintf("at most %d components", maxContextFiles)})
		}
		sel, err := e.Canvas.Resolve(ctx, threadID, p.ContextPaths, "context_paths")
		if err != nil {
			return nil, "", err
		}
		if len(sel) > maxContextFiles {
			return nil, "", apperr.Invalid("invalid input", map[string]string{"context_paths": fmt.Sprintf("at most %d components (the selected folders hold %d)", maxContextFiles, len(sel))})
		}
		cr.selected = sel
	}
	return cr, e.freshFolder(ctx, threadID, defaultFolder(brief)), nil
}

// defaultFolder: every result goes into a new top-level folder named after
// the brief, never into the folder of the components it changes.
func defaultFolder(brief string) string { return canvas.Join("/", canvas.BriefSlug(brief)) }

// freshFolder makes a top-level folder unique among the thread's live folders
// and the destinations of its unfinished runs, so a pending run already shows
// as its own group. The commit makes it unique again against the live files.
func (e *Engine) freshFolder(ctx context.Context, thread, folder string) string {
	taken := map[string]bool{}
	if files, err := e.Canvas.Files(ctx, thread); err == nil {
		for _, f := range files {
			for d := dirOf(f.Path); d != "/"; d = dirOf(d) {
				taken[d] = true
			}
		}
	}
	var active []string
	e.DB.WithContext(ctx).Model(&models.Run{}).
		Where("thread_id = ? AND status IN ?", thread, []string{models.RunPending, models.RunRunning}).
		Pluck("canvas_folder", &active)
	for _, f := range active {
		taken[f] = true
	}
	return canvas.UniqueFolder(folder, func(f string) bool { return taken[f] })
}

func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func baseOf(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// buildCanvasSeed renders the canvas context for a run: the tree, the user's
// changes since the last message, the selected (or latest) components and
// where the result goes. nil when the thread has nothing on its canvas yet.
func (e *Engine) buildCanvasSeed(ctx context.Context, run *models.Run, cr *canvasRun, notes []models.CanvasNote) *portVal {
	if e.Canvas == nil {
		return nil
	}
	files, err := e.Canvas.Files(ctx, run.ThreadID)
	if err != nil {
		e.Log.Warn("engine: canvas files", "thread", run.ThreadID, "err", err)
		return nil
	}
	if len(files) == 0 && len(notes) == 0 && len(cr.selected) == 0 {
		return nil
	}
	msg := e.messageNumbers(ctx, run.ThreadID)

	var b strings.Builder
	b.WriteString("## Canvas\n\nThis chat's design canvas is a tree of HTML component files (folders = groups). ")
	b.WriteString("You can read them under /canvas with your file tools when you have them; you cannot change them.\n")

	b.WriteString("\n### Files\n\n")
	if len(files) == 0 {
		b.WriteString("(the canvas is empty)\n")
	} else {
		b.WriteString(renderTree(files, msg))
	}

	if len(notes) > 0 {
		b.WriteString("\n### Changes the user made since the last message\n\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n.Text)
		}
		b.WriteString("These are no longer part of the design: don't build on, reference or recreate them unless asked.\n")
	}

	switch {
	case len(cr.selected) > 0:
		b.WriteString("\n### Selected components\n\nThe new brief is about these files. Start from their HTML.\n")
		e.writeFiles(ctx, &b, cr.selected)
	case len(files) > 0:
		if latest := latestResult(files); len(latest) > 0 {
			b.WriteString("\n### Latest result\n\nThe most recent design in this chat; build on it if the brief continues it.\n")
			e.writeFiles(ctx, &b, latest)
		}
	}

	if run.Mode == models.RunModeAgent {
		fmt.Fprintf(&b, "\n### Where new components go\n\nExisting files are never modified. Components you create with gen_components become NEW files in a new top-level folder %q", run.CanvasFolder)
		b.WriteString(". Pass gen_components `folder` (a short name for the new group), `names` (one per component) and, ")
		b.WriteString("to build on existing components, `sources` (their canvas paths).\n")
		return &portVal{Text: b.String()}
	}
	fmt.Fprintf(&b, "\n### Where your result goes\n\nNever modify existing files. Your result becomes NEW files in a new top-level folder %q", run.CanvasFolder)
	if len(cr.selected) > 0 {
		b.WriteString(" (its own group, apart from the selected components)")
	}
	b.WriteString(". You may choose a better folder name and file names: call submit_artifact with `folder` ")
	b.WriteString("(one short name for the new group; it always becomes a new top-level folder) and `names` ")
	b.WriteString("(one per component; keep the original file name when you make a new version of one component).\n")
	return &portVal{Text: b.String()}
}

const (
	// maxHistoryMessages is how many earlier messages a chat agent sees.
	maxHistoryMessages = 12
	// maxHistoryReply caps one earlier reply in the history.
	maxHistoryReply = 2000
)

// buildHistory renders the thread's earlier messages for a chat agent: the
// user's text, the reply and the files the message produced (live ones).
// "" for the first message of a chat.
func (e *Engine) buildHistory(ctx context.Context, run *models.Run) string {
	var runs []models.Run
	if err := e.DB.WithContext(ctx).Where("thread_id = ? AND id <> ?", run.ThreadID, run.ID).
		Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
		e.Log.Warn("engine: chat history", "thread", run.ThreadID, "err", err)
		return ""
	}
	if len(runs) == 0 {
		return ""
	}
	filesOf := map[string][]string{}
	if e.Canvas != nil {
		if files, err := e.Canvas.Files(ctx, run.ThreadID); err == nil {
			for _, f := range files {
				filesOf[f.RunID] = append(filesOf[f.RunID], f.Path)
			}
		}
	}
	first := max(0, len(runs)-maxHistoryMessages)
	var b strings.Builder
	b.WriteString("## Conversation so far\n")
	if first > 0 {
		fmt.Fprintf(&b, "\n(%d earlier messages omitted)\n", first)
	}
	for i := first; i < len(runs); i++ {
		r := &runs[i]
		var in map[string]any
		_ = json.Unmarshal(r.Input, &in)
		fmt.Fprintf(&b, "\n### Message %d\n\nUser: %s\n", i+1, briefOf(in))
		if len(r.ContextPaths) > 0 {
			fmt.Fprintf(&b, "(about %s)\n", strings.Join(r.ContextPaths, ", "))
		}
		if r.Vision {
			b.WriteString("(you were shown screenshots of them)\n")
		}
		if reply := strings.TrimSpace(r.Reply); reply != "" {
			fmt.Fprintf(&b, "\nYou: %s\n", truncate(reply, maxHistoryReply))
		}
		if paths := filesOf[r.ID]; len(paths) > 0 {
			fmt.Fprintf(&b, "\nFiles created: %s\n", strings.Join(paths, ", "))
		}
		switch r.Status {
		case models.RunCompleted:
		case models.RunPending, models.RunRunning:
			b.WriteString("\n(still in progress)\n")
		default:
			fmt.Fprintf(&b, "\n(%s: %s)\n", r.Status, r.Error)
		}
	}
	return b.String()
}

// messageNumbers maps the thread's run ids to their 1-based message number.
func (e *Engine) messageNumbers(ctx context.Context, thread string) map[string]int {
	var ids []string
	e.DB.WithContext(ctx).Model(&models.Run{}).Where("thread_id = ?", thread).
		Order("created_at ASC, id ASC").Pluck("id", &ids)
	out := make(map[string]int, len(ids))
	for i, id := range ids {
		out[id] = i + 1
	}
	return out
}

// renderTree lists files as an indented folder tree: in every folder its
// files first, then its subfolders.
func renderTree(files []models.CanvasFile, msg map[string]int) string {
	files = append([]models.CanvasFile(nil), files...)
	sort.SliceStable(files, func(i, j int) bool { return treeLess(files[i].Path, files[j].Path) })
	var b strings.Builder
	var stack []string
	lines := 0
	for i, f := range files {
		if lines >= maxTreeLines {
			fmt.Fprintf(&b, "… and %d more files\n", len(files)-i)
			break
		}
		var segs []string
		if d := dirOf(f.Path); d != "/" {
			segs = strings.Split(d[1:], "/")
		}
		common := 0
		for common < len(stack) && common < len(segs) && stack[common] == segs[common] {
			common++
		}
		stack = stack[:common]
		for d := common; d < len(segs); d++ {
			if d == 0 {
				fmt.Fprintf(&b, "/%s/\n", segs[d])
			} else {
				fmt.Fprintf(&b, "%s%s/\n", strings.Repeat("  ", d), segs[d])
			}
			stack = append(stack, segs[d])
			lines++
		}
		var meta []string
		if f.Score != nil {
			meta = append(meta, fmt.Sprintf("score %.1f", *f.Score))
		}
		if n := msg[f.RunID]; n > 0 {
			meta = append(meta, fmt.Sprintf("message %d", n))
		}
		line := strings.Repeat("  ", len(segs)) + baseOf(f.Path)
		if len(meta) > 0 {
			line += " — " + strings.Join(meta, ", ")
		}
		b.WriteString(line + "\n")
		lines++
	}
	return b.String()
}

// treeLess orders paths folder by folder, files before subfolders.
func treeLess(a, b string) bool {
	as, bs := strings.Split(a[1:], "/"), strings.Split(b[1:], "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		aFile, bFile := i == len(as)-1, i == len(bs)-1
		if aFile != bFile {
			return aFile
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}

// latestResult is the live files of the most recent run that committed any.
func latestResult(files []models.CanvasFile) []models.CanvasFile {
	var newest *models.CanvasFile
	for i := range files {
		if newest == nil || files[i].CreatedAt.After(newest.CreatedAt) {
			newest = &files[i]
		}
	}
	if newest == nil {
		return nil
	}
	var out []models.CanvasFile
	for _, f := range files {
		if f.RunID == newest.RunID && len(out) < maxLatestFiles {
			out = append(out, f)
		}
	}
	return out
}

// writeFiles appends each file's path and fenced HTML (shared budget, at least
// 2 000 characters per file).
func (e *Engine) writeFiles(ctx context.Context, b *strings.Builder, files []models.CanvasFile) {
	budget := maxContextHTML
	for _, f := range files {
		raw, err := e.Canvas.Content(ctx, f)
		if err != nil {
			fmt.Fprintf(b, "\n#### %s\n\n(content unavailable)\n", f.Path)
			continue
		}
		html := truncate(string(raw), max(budget, 2000))
		budget -= len(html)
		fmt.Fprintf(b, "\n#### %s\n\n```html\n%s\n```\n", f.Path, html)
	}
}

/* ---------- commit ---------- */

// destHint is the agent's choice of folder / file names for the result, plus
// the canvas file each result is a new version of (gen_components sources).
type destHint struct {
	folder  string
	names   []string
	sources []string
}

func (d destHint) empty() bool {
	return d.folder == "" && len(d.names) == 0 && strings.Join(d.sources, "") == ""
}

// commitCanvas turns a completed run's results into canvas files (inside the
// transaction carried by ctx) and returns their paths.
func (e *Engine) commitCanvas(ctx context.Context, run *models.Run, st *runState, results []models.ResultArtifact) ([]string, error) {
	if e.Canvas == nil || run.ThreadID == "" || len(results) == 0 {
		return nil, nil
	}
	cr := st.canvas
	if cr == nil {
		cr = &canvasRun{}
	}
	hint := st.dest()

	dest := canvas.Dest{Folder: run.CanvasFolder, Fresh: true}
	if dest.Folder == "" {
		dest.Folder = defaultFolder(cr.brief)
	}
	// The agent may name the group, but results never join an existing folder.
	if f := canvas.TopFolder(hint.folder); f != "" {
		dest.Folder = f
	}

	source := ""
	if len(cr.selected) == 1 {
		source = cr.selected[0].Path
	}
	slug := canvas.BriefSlug(cr.brief)
	items := make([]canvas.CommitItem, len(results))
	for i, r := range results {
		source := source
		if i < len(hint.sources) && hint.sources[i] != "" {
			source = hint.sources[i]
		}
		name := ""
		switch {
		case i < len(hint.names) && strings.TrimSpace(hint.names[i]) != "":
			name = hint.names[i]
		case len(results) == 1 && source != "":
			name = baseOf(source)
		case len(results) == 1:
			name = slug + ".html"
		default:
			name = fmt.Sprintf("%s-%d.html", slug, i+1)
		}
		items[i] = canvas.CommitItem{Name: name, ArtifactID: r.ArtifactID, Score: r.Score, SourcePath: source}
	}
	folder, files, err := e.Canvas.Commit(ctx, run.ThreadID, run.ID, dest, items)
	if err != nil {
		return nil, err
	}
	run.CanvasFolder = folder
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths, nil
}
