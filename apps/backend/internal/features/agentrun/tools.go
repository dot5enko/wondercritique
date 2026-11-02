// Agent tool construction (plan §4.4 allowlist → catalog tools). File tools
// and run_js operate on a SESSION-scoped VFS: /work read-write (one sqlite
// workspace per session id) plus read-only /skills/{slug}/ and /rules/ mounts.
package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/sandbox/jsrun"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tools/fstools"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// sessionFS resolves the per-session mount on every call: the session id comes
// from the tool call context (agentkit.CallFromContext).
type sessionFS struct {
	runtime *Runtime
}

var _ vfs.FS = sessionFS{}

// mountFor builds (or reuses) the mount tree for one session.
func (s sessionFS) mountFor(ctx context.Context, sessionID string) (vfs.FS, error) {
	mnt, ok, err := s.runtime.sessionMount(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, vfs.ErrNotFound
	}
	return mnt, nil
}

func (s sessionFS) session(ctx context.Context) (string, error) {
	call, ok := agentkit.CallFromContext(ctx)
	if !ok || call.SessionID == "" {
		return "", fmt.Errorf("no session in tool context")
	}
	return call.SessionID, nil
}

func (s sessionFS) Stat(ctx context.Context, path string) (vfs.FileInfo, error) {
	id, err := s.session(ctx)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	mnt, err := s.mountFor(ctx, id)
	if err != nil {
		return vfs.FileInfo{}, err
	}
	return mnt.Stat(ctx, path)
}

func (s sessionFS) Read(ctx context.Context, path string) ([]byte, error) {
	id, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	mnt, err := s.mountFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return mnt.Read(ctx, path)
}

func (s sessionFS) Write(ctx context.Context, path string, data []byte) error {
	id, err := s.session(ctx)
	if err != nil {
		return err
	}
	mnt, err := s.mountFor(ctx, id)
	if err != nil {
		return err
	}
	return mnt.Write(ctx, path, data)
}

func (s sessionFS) Delete(ctx context.Context, path string) error {
	id, err := s.session(ctx)
	if err != nil {
		return err
	}
	mnt, err := s.mountFor(ctx, id)
	if err != nil {
		return err
	}
	return mnt.Delete(ctx, path)
}

func (s sessionFS) List(ctx context.Context, dir string, recursive bool) ([]vfs.FileInfo, error) {
	id, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	mnt, err := s.mountFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return mnt.List(ctx, dir, recursive)
}

// buildTools assembles the agent's tools from its allowlist. The output
// contract decides which terminal submit tool is exposed.
func (r *Runtime) buildTools(res *resolvedBody) (*tool.Set, error) {
	body := res.Body
	set := tool.NewSet()
	fsys := sessionFS{runtime: r}
	env := toolEnv{r: r, fsys: fsys, self: res}
	for _, name := range body.Tools {
		switch name {
		case "read_file", "write_file", "edit_file", "glob", "grep":
			set.Add(fstools.New(fsys, fstools.Options{})...)
		case "run_js":
			set.Add(jsrun.NewTool(r.js, fsys))
		case "validate_component":
			set.Add(validateComponentTool())
		case "generate_components":
			set.Add(env.generateTool())
		case "pick_best":
			set.Add(env.pickBestTool())
		case "critique_component":
			set.Add(env.critiqueTool())
		case "submit_artifact", "submit_critique", "submit_fields":
			// Terminal submit tools are added once, below, per contract.
		default:
			return nil, fmt.Errorf("agentrun: unknown tool %q", name)
		}
	}
	if term := submitTool(body.OutputContract, fsys); term != nil {
		set.Add(tool.AsTerminal(term))
	}
	return set, nil
}

// validateComponentTool wraps component.Validate: input {html} → formatted report.
func validateComponentTool() tool.Tool {
	type input struct {
		HTML string `json:"html" jsonschema_description:"HTML component fragment to validate"`
	}
	return tool.New("validate_component",
		"Validate an HTML component fragment against the platform rules and return the violation report.",
		func(ctx context.Context, in input) (string, error) {
			rep := component.Validate(in.HTML)
			out := struct {
				OK         bool                  `json:"ok"`
				Violations []component.Violation `json:"violations"`
				Fragment   component.Fragment    `json:"fragment"`
				Text       string                `json:"text"`
			}{OK: rep.OK(), Violations: rep.Violations, Fragment: rep.Fragment}
			out.Text = component.Format(rep.Violations)
			raw, err := json.Marshal(out)
			if err != nil {
				return "", err
			}
			return string(raw), nil
		})
}

// submitTool builds the terminal submit tool for the output contract; nil when
// the contract type is unknown. The tool's output (returned to the caller as
// the run's final answer) is the submitted payload itself.
func submitTool(contract studio.OutputContract, fsys vfs.FS) tool.Tool {
	switch contract.Type {
	case "artifact":
		type in struct {
			HTML  string   `json:"html,omitempty" jsonschema_description:"The complete HTML component fragment"`
			Path  string   `json:"path,omitempty" jsonschema_description:"Instead of html: a workspace file holding the component, e.g. /work/candidates/c2.html (or just c2)"`
			Paths []string `json:"paths,omitempty" jsonschema_description:"Several components as the result (one file each), e.g. when the user asked for 5 buttons"`
		}
		return tool.New("submit_artifact",
			"Submit the final HTML component and finish the task. Pass exactly one of html, path or paths (paths = several components as the result).",
			func(ctx context.Context, i in) (string, error) {
				set := 0
				for _, b := range []bool{i.HTML != "", i.Path != "", len(i.Paths) > 0} {
					if b {
						set++
					}
				}
				if set != 1 {
					return "", fmt.Errorf("pass exactly one of html, path or paths")
				}
				switch {
				case i.HTML != "":
					return i.HTML, nil
				case i.Path != "":
					return readComponent(ctx, fsys, i.Path)
				}
				items := make([]string, 0, len(i.Paths))
				for _, p := range i.Paths {
					h, err := readComponent(ctx, fsys, p)
					if err != nil {
						return "", err
					}
					items = append(items, h)
				}
				if len(items) == 1 {
					return items[0], nil
				}
				raw, err := json.Marshal(map[string][]string{componentSetKey: items})
				if err != nil {
					return "", err
				}
				return string(raw), nil
			})
	case "critique":
		type issue struct {
			Severity   string `json:"severity" jsonschema_description:"info, warning or error"`
			Code       string `json:"code,omitempty" jsonschema_description:"Short stable code, e.g. spacing"`
			Message    string `json:"message" jsonschema_description:"What is wrong"`
			Suggestion string `json:"suggestion,omitempty" jsonschema_description:"How to fix it"`
		}
		type in struct {
			Strengths []string `json:"strengths" jsonschema_description:"What works well"`
			Issues    []issue  `json:"issues" jsonschema_description:"Concrete problems"`
			Score     float64  `json:"score" jsonschema_description:"Overall quality, 0-10"`
		}
		return tool.New("submit_critique",
			"Submit the structured critique (strengths, issues, score) and finish the task.",
			func(ctx context.Context, i in) (string, error) {
				raw, err := json.Marshal(i)
				if err != nil {
					return "", err
				}
				return string(raw), nil
			})
	case "custom":
		return submitFieldsTool(contract.FieldsSchema)
	}
	return nil
}

// submitFieldsTool builds a submit_fields tool whose schema is the contract's
// fields_schema (dynamic: built from the raw JSON schema, not a Go struct).
func submitFieldsTool(schema json.RawMessage) tool.Tool {
	spec := llm.ToolSpec{
		Name:        "submit_fields",
		Description: "Submit the required structured fields and finish the task.",
		Schema:      schema,
	}
	if len(spec.Schema) == 0 {
		spec.Schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return tool.Func{
		ToolSpec: spec,
		Fn: func(ctx context.Context, input json.RawMessage) (string, error) {
			if !json.Valid(input) {
				return "", fmt.Errorf("submit_fields input must be a JSON object")
			}
			return string(input), nil
		},
	}
}

/* ---------- session mounts ---------- */

// maxMountedSessions caps the per-session mount cache.
const maxMountedSessions = 256

// agentMaterial is the read-only VFS content of one built agent, recorded
// under the agent name when the agent is built (sessions bind to names).
type agentMaterial struct {
	skills  []skillMount
	rulesMD string
}

type skillMount struct {
	slug  string
	files map[string][]byte // relative paths (no leading /)
}

// sessionMount builds the VFS for one session: /work rw (sqlite workspace),
// /skills/{slug}/... ro (the agent's skill defs), /rules ro (the agent's rules
// dumped as markdown). Mounted lazily per session with a small FIFO cache.
func (r *Runtime) sessionMount(ctx context.Context, sessionID string) (vfs.FS, bool, error) {
	r.mountMu.Lock()
	mnt, ok := r.mounts[sessionID]
	r.mountMu.Unlock()
	if ok {
		return mnt, true, nil
	}

	// Which agent owns this session? Shared-store sessions record the agent
	// name; try-run sessions are looked up in the ephemeral map.
	sess, err := r.agentStore.GetSession(ctx, sessionID)
	agentName := ""
	if err == nil {
		agentName = sess.Agent
	} else {
		r.ephemeralMu.RLock()
		agentName = r.ephemeral[sessionID]
		r.ephemeralMu.RUnlock()
		if agentName == "" {
			return nil, false, nil // unknown session: no mounts
		}
	}
	r.materialMu.RLock()
	material, ok := r.material[agentName]
	r.materialMu.RUnlock()
	if !ok {
		return nil, false, nil
	}

	points := []vfs.MountPoint{{
		Prefix: "/work",
		FS:     r.vfsStore.Workspace("session:"+sessionID, vfs.Limits{}),
	}}
	for _, sk := range material.skills {
		points = append(points, vfs.MountPoint{
			Prefix: "/skills/" + sk.slug,
			FS:     vfs.ReadOnly(memoryFS(sk.files)),
		})
	}
	if material.rulesMD != "" {
		points = append(points, vfs.MountPoint{
			Prefix: "/rules",
			FS:     vfs.ReadOnly(memoryFS(map[string][]byte{"rules.md": []byte(material.rulesMD)})),
		})
	}
	mnt, err = vfs.NewMount(points...)
	if err != nil {
		return nil, false, fmt.Errorf("agentrun: mount session vfs: %w", err)
	}

	r.mountMu.Lock()
	if r.mounts == nil {
		r.mounts = map[string]vfs.FS{}
	}
	r.mounts[sessionID] = mnt
	if len(r.mountOrder) >= maxMountedSessions {
		// Simple FIFO cleanup of the oldest mount.
		old := r.mountOrder[0]
		r.mountOrder = r.mountOrder[1:]
		delete(r.mounts, old)
	}
	r.mountOrder = append(r.mountOrder, sessionID)
	r.mountMu.Unlock()
	return mnt, true, nil
}

// memoryFS seeds an in-memory FS with the given relative-path files.
func memoryFS(files map[string][]byte) vfs.FS {
	fs := memory.New(vfs.Limits{MaxFileSize: 4 << 20, MaxTotalSize: 16 << 20, MaxFiles: 500})
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		_ = fs.Write(context.Background(), "/"+p, files[p]) // content is trusted (published defs)
	}
	return fs
}

// rulesMarkdown renders the agent's rule groups as a markdown document.
func rulesMarkdown(groups []studio.RuleGroup) string {
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "## %s\n\n", g.Name)
		for _, r := range g.Rules {
			fmt.Fprintf(&b, "- [%s] %s\n", r.Severity, r.Text)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
