// The multi-candidate agent tools: generate_components, pick_best and
// critique_component. Candidates live in the calling session's workspace as
// /work/candidates/c{N}.html; inside a pipeline step the tools also report to
// the step's Sink (artifacts + sub-run usage).
package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// candidateDir is where generate_components writes its candidates.
const candidateDir = "/work/candidates"

// candidateTools are stripped from worker builds (no recursive fan-out).
var candidateTools = map[string]bool{"generate_components": true, "pick_best": true, "critique_component": true, "gen_components": true}

func withoutCandidateTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if !candidateTools[t] {
			out = append(out, t)
		}
	}
	return out
}

// toolTimeout gives the multi-candidate tools room for nested agent runs.
func toolTimeout(tools []string) time.Duration {
	for _, t := range tools {
		if candidateTools[t] {
			return 20 * time.Minute
		}
	}
	return 0 // agentkit default
}

// toolEnv is what the multi-candidate tools know about the agent they belong to.
type toolEnv struct {
	r    *Runtime
	fsys vfs.FS
	self *resolvedBody
}

// owner is the user the nested runs are attributed to.
func (e toolEnv) owner(ctx context.Context) string {
	if sc, ok := scopeFrom(ctx); ok && sc.owner != "" {
		return sc.owner
	}
	if call, ok := agentkit.CallFromContext(ctx); ok && call.SessionID != "" {
		if sess, err := e.r.agentStore.GetSession(ctx, call.SessionID); err == nil {
			if o, ok := sess.Metadata["owner"].(string); ok {
				return o
			}
		}
	}
	return ""
}

// thread is the chat thread of the calling run ("" outside a pipeline).
func (e toolEnv) thread(ctx context.Context) string {
	if sc, ok := scopeFrom(ctx); ok {
		return sc.thread
	}
	return ""
}

// baseSamples are the calling run's prompt variables (empty outside RunNode).
func baseSamples(ctx context.Context) map[string]any {
	out := map[string]any{}
	if sc, ok := scopeFrom(ctx); ok {
		for k, v := range sc.samples {
			out[k] = v
		}
	}
	return out
}

// agentRef resolves a configured agent id, else a default slug.
func (e toolEnv) agentRef(ctx context.Context, id, slug string) (string, error) {
	if id != "" {
		return id, nil
	}
	def, err := e.r.store.GetBySlug(ctx, studio.KindAgent, slug)
	if err != nil {
		return "", fmt.Errorf("no %q agent exists; create it in Studio or configure the tool in this agent's tool settings", slug)
	}
	return def.ID, nil
}

func (e toolEnv) guard(ctx context.Context, name string) error {
	if sc, ok := scopeFrom(ctx); ok && sc.worker {
		return fmt.Errorf("%s is not available inside a nested candidate run", name)
	}
	return nil
}

func (e toolEnv) putShot(ctx context.Context, png []byte) (string, error) {
	if s := sinkFrom(ctx); s != nil {
		if _, err := s.Artifact(ctx, models.ArtifactImage, "image/png", png, nil); err != nil {
			return "", err
		}
	}
	sha, err := e.r.blobs.Put(context.WithoutCancel(ctx), png, "image/png")
	if err != nil {
		return "", err
	}
	return blobs.URL(sha), nil
}

func toJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

/* ---------- candidate files ---------- */

var candidateIDRe = regexp.MustCompile(`^c([0-9]{1,4})$`)

// candidatePath maps "c3", "c3.html" or a path to a /work path.
func candidatePath(ref string) string {
	ref = strings.TrimSpace(ref)
	base := strings.TrimSuffix(ref, ".html")
	if candidateIDRe.MatchString(base) {
		return path.Join(candidateDir, base+".html")
	}
	if !strings.HasPrefix(ref, "/") {
		return path.Join("/work", ref)
	}
	return ref
}

// candidateID is the short id of a candidate path ("" for other paths).
func candidateID(p string) string {
	if path.Dir(p) != candidateDir {
		return ""
	}
	return strings.TrimSuffix(path.Base(p), ".html")
}

// readComponent reads a component file from the session workspace.
func readComponent(ctx context.Context, fsys vfs.FS, ref string) (string, error) {
	p := candidatePath(ref)
	raw, err := fsys.Read(ctx, p)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", p, err)
	}
	return string(raw), nil
}

// listCandidates returns the candidate ids present in the workspace, ordered.
func listCandidates(ctx context.Context, fsys vfs.FS) ([]int, error) {
	infos, err := fsys.List(ctx, candidateDir, false)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var ids []int
	for _, fi := range infos {
		m := candidateIDRe.FindStringSubmatch(strings.TrimSuffix(path.Base(fi.Path), ".html"))
		if m == nil || !strings.HasSuffix(fi.Path, ".html") {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		ids = append(ids, n)
	}
	sort.Ints(ids)
	return ids, nil
}

/* ---------- generate_components ---------- */

type generatedInfo struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Variation  string `json:"variation,omitempty"`
	Bytes      int    `json:"bytes"`
	Valid      bool   `json:"valid"`
	Violations string `json:"violations,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

func (e toolEnv) generateTool() tool.Tool {
	type input struct {
		Count      int      `json:"count" jsonschema_description:"How many candidates to generate in parallel"`
		Brief      string   `json:"brief,omitempty" jsonschema_description:"Brief for the candidates; defaults to the current brief"`
		Variations []string `json:"variations,omitempty" jsonschema_description:"Optional direction hint per candidate (e.g. minimal, bold color, glassmorphism)"`
	}
	cfg := e.self.Body.GenerateConfig()
	maxCount := cfg.MaxCount
	if maxCount <= 0 {
		maxCount = studio.DefaultCandidateCount
	}
	maxCount = min(maxCount, studio.MaxCandidates)
	desc := fmt.Sprintf("Generate several candidate HTML components in parallel (1-%d). Each is written to %s/c{N}.html; "+
		"then compare them with pick_best, review one with critique_component, read them with read_file, "+
		"and finish with submit_artifact (path for one, paths for several).", maxCount, candidateDir)

	return tool.New("generate_components", desc, func(ctx context.Context, in input) (string, error) {
		if err := e.guard(ctx, "generate_components"); err != nil {
			return "", err
		}
		if in.Count < 1 {
			return "", errors.New("count must be at least 1")
		}
		if in.Count > maxCount {
			return "", fmt.Errorf("count must be at most %d", maxCount)
		}
		base := NodeRequest{Owner: e.owner(ctx), ThreadID: e.thread(ctx), Title: "candidates", Samples: baseSamples(ctx)}
		if strings.TrimSpace(in.Brief) != "" {
			base.Samples["brief"] = in.Brief
		}
		if cfg.AgentID != "" {
			base.AgentID = cfg.AgentID
			base.Draft = e.self.Draft
		} else {
			// The calling agent itself, as a worker (without these tools).
			base.AgentID = e.self.Def.ID
			if e.self.Draft {
				base.Draft = true
			} else {
				base.Version = versionOf(e.self.Def)
			}
		}

		cands, err := GenerateCandidates(ctx, e.r.RunNode, GenerateRequest{Base: base, Count: in.Count, Variations: in.Variations})
		sink := sinkFrom(ctx)
		for _, c := range cands {
			if sink != nil && c.Result != nil {
				sink.SubRun(c.Result)
			}
		}
		if err != nil {
			return "", err
		}
		existing, lerr := listCandidates(ctx, e.fsys)
		if lerr != nil {
			return "", lerr
		}
		next := 1
		if len(existing) > 0 {
			next = existing[len(existing)-1] + 1
		}

		out := struct {
			Candidates []generatedInfo  `json:"candidates"`
			Failed     []map[string]any `json:"failed,omitempty"`
			Next       string           `json:"next"`
		}{Candidates: []generatedInfo{}}
		for _, c := range cands {
			if c.Err != nil {
				out.Failed = append(out.Failed, map[string]any{"candidate": c.Index, "variation": c.Variation, "error": c.Err.Error()})
				continue
			}
			id := fmt.Sprintf("c%d", next)
			next++
			p := path.Join(candidateDir, id+".html")
			if err := e.fsys.Write(ctx, p, []byte(c.HTML)); err != nil {
				return "", fmt.Errorf("write %s: %w", p, err)
			}
			rep := component.Validate(c.HTML)
			info := generatedInfo{ID: id, Path: p, Variation: c.Variation, Bytes: len(c.HTML), Valid: rep.OK()}
			if !rep.OK() {
				info.Violations = truncateStr(component.Format(rep.Violations), 600)
			}
			if sink != nil {
				aid, err := sink.Artifact(ctx, models.ArtifactHTML, "text/html; charset=utf-8", []byte(c.HTML), nil)
				if err != nil {
					return "", err
				}
				info.ArtifactID = aid
			}
			out.Candidates = append(out.Candidates, info)
		}
		if sink != nil {
			sink.Event("generated", out.Candidates)
		}
		out.Next = "Compare with pick_best, or submit_artifact with path (one) / paths (several)."
		return toJSON(out)
	})
}

/* ---------- pick_best ---------- */

func (e toolEnv) pickBestTool() tool.Tool {
	type input struct {
		Candidates []string `json:"candidates,omitempty" jsonschema_description:"Candidate ids (c1, c2…) or /work paths; default: every file in /work/candidates"`
		Criteria   string   `json:"criteria,omitempty" jsonschema_description:"What matters most for this choice"`
		Brief      string   `json:"brief,omitempty" jsonschema_description:"Brief to judge against; defaults to the current brief"`
	}
	cfg := e.self.Body.PickBestConfig()
	return tool.New("pick_best",
		"Render the candidates and let a vision judge compare the screenshots side by side. Returns the winner, a ranking with scores and reasons, and a summary.",
		func(ctx context.Context, in input) (string, error) {
			if err := e.guard(ctx, "pick_best"); err != nil {
				return "", err
			}
			refs := in.Candidates
			if len(refs) == 0 {
				ids, err := listCandidates(ctx, e.fsys)
				if err != nil {
					return "", err
				}
				for _, n := range ids {
					refs = append(refs, fmt.Sprintf("c%d", n))
				}
			}
			if len(refs) < 2 {
				return "", errors.New("pick_best needs at least two candidates (call generate_components first)")
			}
			if len(refs) > studio.MaxCandidates {
				return "", fmt.Errorf("pick_best compares at most %d candidates", studio.MaxCandidates)
			}
			judgeID, err := e.agentRef(ctx, cfg.JudgeAgentID, studio.DefaultJudgeSlug)
			if err != nil {
				return "", err
			}

			paths := make([]string, len(refs))
			entries := make([]JudgeCandidate, len(refs))
			for i, ref := range refs {
				paths[i] = candidatePath(ref)
				html, err := readComponent(ctx, e.fsys, ref)
				if err != nil {
					return "", err
				}
				entries[i] = JudgeCandidate{HTML: html}
				activity(ctx, "Rendering candidate %d/%d", i+1, len(refs))
				shots, rerr := RenderShots(ctx, e.r.renderer, html, []render.Variant{judgeVariant()}, e.putShot)
				if rerr != nil {
					entries[i].Note = rerr.Error()
				}
				entries[i].Images = shots
			}

			brief := in.Brief
			if brief == "" {
				brief, _ = baseSamples(ctx)["brief"].(string)
			}
			v, err := Judge(ctx, e.r.RunNode, JudgeRequest{
				Base:       NodeRequest{AgentID: judgeID, Draft: e.self.Draft, Owner: e.owner(ctx), ThreadID: e.thread(ctx), Title: "pick_best"},
				Brief:      brief,
				Criteria:   in.Criteria,
				Candidates: entries,
			})
			sink := sinkFrom(ctx)
			if sink != nil && v != nil && v.Result != nil {
				sink.SubRun(v.Result)
			}
			if err != nil {
				return "", err
			}

			type ranked struct {
				ID     string  `json:"id"`
				Path   string  `json:"path"`
				Score  float64 `json:"score"`
				Reason string  `json:"reason"`
			}
			out := struct {
				Winner     string   `json:"winner"`
				WinnerPath string   `json:"winner_path"`
				Ranking    []ranked `json:"ranking"`
				Summary    string   `json:"summary"`
				Next       string   `json:"next"`
			}{Summary: v.Summary, Ranking: []ranked{}}
			label := func(n int) (string, string) {
				p := paths[n-1]
				if id := candidateID(p); id != "" {
					return id, p
				}
				return p, p
			}
			out.Winner, out.WinnerPath = label(v.Winner)
			for _, r := range v.Ranking {
				id, p := label(r.Candidate)
				out.Ranking = append(out.Ranking, ranked{ID: id, Path: p, Score: r.Score, Reason: r.Reason})
			}
			out.Next = fmt.Sprintf("Submit with submit_artifact {\"path\": %q}, or improve it first.", out.WinnerPath)
			if sink != nil {
				raw, _ := json.Marshal(out)
				if _, err := sink.Artifact(ctx, models.ArtifactJSON, "application/json", raw, v.WinnerScore()); err != nil {
					return "", err
				}
				sink.Event("picked", out)
			}
			return toJSON(out)
		})
}

// judgeVariant is the screenshot variant judges and tool critics see.
func judgeVariant() render.Variant {
	return render.Variant{Width: 1280, ColorScheme: "light", DeviceScaleFactor: 1}
}

/* ---------- critique_component ---------- */

func (e toolEnv) critiqueTool() tool.Tool {
	type input struct {
		Candidate string `json:"candidate,omitempty" jsonschema_description:"Candidate id (c1…) or /work path of the component"`
		HTML      string `json:"html,omitempty" jsonschema_description:"Instead of candidate: the HTML fragment itself"`
		Brief     string `json:"brief,omitempty" jsonschema_description:"Brief to judge against; defaults to the current brief"`
		Focus     string `json:"focus,omitempty" jsonschema_description:"Optional aspects to focus the critique on"`
	}
	cfg := e.self.Body.CritiqueConfig()
	return tool.New("critique_component",
		"Render one component and get a structured critique (strengths, issues, 0-10 score) from a critic agent.",
		func(ctx context.Context, in input) (string, error) {
			if err := e.guard(ctx, "critique_component"); err != nil {
				return "", err
			}
			if (in.Candidate == "") == (in.HTML == "") {
				return "", errors.New("pass exactly one of candidate or html")
			}
			html := in.HTML
			if in.Candidate != "" {
				var err error
				if html, err = readComponent(ctx, e.fsys, in.Candidate); err != nil {
					return "", err
				}
			}
			criticID, err := e.agentRef(ctx, cfg.CriticAgentID, studio.DefaultCriticSlug)
			if err != nil {
				return "", err
			}
			samples := baseSamples(ctx)
			delete(samples, "feedback")
			delete(samples, "critique")
			if in.Brief != "" {
				samples["brief"] = in.Brief
			}
			samples["html"] = html
			req := NodeRequest{AgentID: criticID, Draft: e.self.Draft, Owner: e.owner(ctx), ThreadID: e.thread(ctx), Title: "critique_component", Worker: true, Samples: samples}
			activity(ctx, "Rendering the component")
			shots, rerr := RenderShots(ctx, e.r.renderer, html, []render.Variant{judgeVariant()}, e.putShot)
			activity(ctx, "Critic reviewing the render")
			if len(shots) > 0 {
				samples["images"] = shots
			}
			if rerr != nil {
				req.Extra = append(req.Extra, llm.TextBlock("Note: no screenshot is available ("+rerr.Error()+"); critique the HTML source:\n\n```html\n"+truncateStr(html, maxJudgeHTML)+"\n```"))
			}
			if in.Focus != "" {
				req.Extra = append(req.Extra, llm.TextBlock("## Focus\n\n"+in.Focus))
			}
			res, err := e.r.RunNode(ctx, req)
			sink := sinkFrom(ctx)
			if sink != nil && res != nil {
				sink.SubRun(res)
			}
			if err != nil {
				return "", err
			}
			if res.Critique == nil {
				return "", fmt.Errorf("critic %q must use the critique output contract", res.AgentSlug)
			}
			if sink != nil {
				score := res.Critique.Score
				if _, err := sink.Artifact(ctx, models.ArtifactJSON, "application/json", jsonBytes(res.Critique), &score); err != nil {
					return "", err
				}
			}
			return toJSON(res.Critique)
		})
}

func jsonBytes(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
