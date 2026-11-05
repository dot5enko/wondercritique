// gen_components: the chat agent's generic generation tool. It runs a worker
// generator agent `count` times in parallel on a description and reports every
// component to the step's Sink as a result of the run — nothing is judged or
// dropped. Copies land in the calling session's /work/components/.
package agentrun

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// componentDir is where gen_components copies its components.
const componentDir = "/work/components"

// maxSourceHTML caps the HTML of one source component shown to the worker.
const maxSourceHTML = 30000

type genInfo struct {
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
	Variation  string `json:"variation,omitempty"`
	Bytes      int    `json:"bytes"`
	Valid      bool   `json:"valid"`
	Violations string `json:"violations,omitempty"`
}

func (e toolEnv) genTool() tool.Tool {
	type input struct {
		Count       int      `json:"count,omitempty" jsonschema_description:"How many components to create (default 1). Use the number the user asked for: 5 variants = 5."`
		Description string   `json:"description" jsonschema_description:"Complete brief for the generator: what to build, content, style and constraints. The generator does not see the chat."`
		Variations  []string `json:"variations,omitempty" jsonschema_description:"Optional direction per component (index i = component i+1), e.g. minimal, bold color, glassmorphism"`
		Sources     []string `json:"sources,omitempty" jsonschema_description:"Canvas paths of existing components to build on, e.g. /pricing/tiers.html (they are never modified)"`
		Folder      string   `json:"folder,omitempty" jsonschema_description:"Short name of the new canvas group for these components, e.g. pricing-dark"`
		Names       []string `json:"names,omitempty" jsonschema_description:"File names, one per component, e.g. tiers.html"`
	}
	cfg := e.self.Body.GenConfig()
	maxCount := cfg.MaxCount
	if maxCount <= 0 {
		maxCount = studio.MaxCandidates
	}
	maxCount = min(maxCount, studio.MaxCandidates)
	desc := fmt.Sprintf("Create components from a description (count 1-%d, default 1). Every component is kept and "+
		"shown on the canvas as a result of this message; copies are written to %s/ for read_file.", maxCount, componentDir)

	return tool.New("gen_components", desc, func(ctx context.Context, in input) (string, error) {
		if err := e.guard(ctx, "gen_components"); err != nil {
			return "", err
		}
		count := max(in.Count, 1)
		if count > maxCount {
			return "", fmt.Errorf("count must be at most %d", maxCount)
		}
		if strings.TrimSpace(in.Description) == "" {
			return "", errors.New("description is required")
		}
		workerID, err := e.agentRef(ctx, cfg.AgentID, studio.DefaultWorkerSlug)
		if err != nil {
			return "", err
		}

		samples := baseSamples(ctx)
		for _, k := range []string{"canvas", "history", "feedback", "critique", "html"} {
			delete(samples, k)
		}
		samples["brief"] = strings.TrimSpace(in.Description)
		base := NodeRequest{AgentID: workerID, Draft: e.self.Draft, Owner: e.owner(ctx), ThreadID: e.thread(ctx), StyleguideID: e.styleguide(ctx), Title: "gen_components", Samples: samples}

		sources := make([]string, 0, len(in.Sources))
		if len(in.Sources) > 0 {
			var b strings.Builder
			b.WriteString("## Source components\n\nBuild on these existing components (make new versions; keep what the brief doesn't change).\n")
			for _, s := range in.Sources {
				p := canvasPath(s)
				raw, err := e.fsys.Read(ctx, "/canvas"+p)
				if err != nil {
					return "", fmt.Errorf("source %s: %w", p, err)
				}
				fmt.Fprintf(&b, "\n### %s\n\n```html\n%s\n```\n", p, truncateStr(string(raw), maxSourceHTML))
				sources = append(sources, p)
			}
			base.Extra = []llm.Block{llm.TextBlock(b.String())}
		}

		cands, err := GenerateCandidates(ctx, e.r.RunNode, GenerateRequest{Base: base, Count: count, Variations: in.Variations})
		sink := sinkFrom(ctx)
		for _, c := range cands {
			if sink != nil && c.Result != nil {
				sink.SubRun(c.Result)
			}
		}
		if err != nil {
			return "", err
		}

		source := ""
		if len(sources) == 1 {
			source = sources[0]
		}
		out := struct {
			Components []genInfo        `json:"components"`
			Failed     []map[string]any `json:"failed,omitempty"`
			Note       string           `json:"note"`
		}{Components: []genInfo{}}
		for i, c := range cands {
			if c.Err != nil {
				out.Failed = append(out.Failed, map[string]any{"component": c.Index, "variation": c.Variation, "error": c.Err.Error()})
				continue
			}
			name := ""
			if i < len(in.Names) {
				name = strings.TrimSpace(in.Names[i])
			}
			p, err := e.writeComponent(ctx, name, c.HTML)
			if err != nil {
				return "", err
			}
			rep := component.Validate(c.HTML)
			info := genInfo{Path: p, Name: name, Variation: c.Variation, Bytes: len(c.HTML), Valid: rep.OK()}
			if !rep.OK() {
				info.Violations = truncateStr(component.Format(rep.Violations), 600)
			}
			if sink != nil {
				if _, err := sink.Component(ctx, Component{HTML: c.HTML, Name: name, Folder: strings.TrimSpace(in.Folder), Variation: c.Variation, Source: source}); err != nil {
					return "", err
				}
			}
			out.Components = append(out.Components, info)
		}
		out.Note = fmt.Sprintf("%d component(s) created; they are on the canvas as results of this message.", len(out.Components))
		return toJSON(out)
	})
}

// writeComponent copies a component into the session workspace under its
// name (or component-N.html), never overwriting an earlier one.
func (e toolEnv) writeComponent(ctx context.Context, name, html string) (string, error) {
	stem := strings.TrimSuffix(path.Base(strings.TrimSpace(name)), ".html")
	if stem == "" || stem == "." || stem == "/" {
		stem = "component"
	}
	for n := 1; ; n++ {
		file := stem + ".html"
		if n > 1 || stem == "component" {
			file = fmt.Sprintf("%s-%d.html", stem, n)
		}
		p := path.Join(componentDir, file)
		if _, err := e.fsys.Stat(ctx, p); err == nil {
			continue
		} else if !errors.Is(err, vfs.ErrNotFound) {
			return "", err
		}
		if err := e.fsys.Write(ctx, p, []byte(html)); err != nil {
			return "", fmt.Errorf("write %s: %w", p, err)
		}
		return p, nil
	}
}

// canvasPath accepts a canvas path with or without the /canvas mount prefix.
func canvasPath(p string) string {
	p = "/" + strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "/canvas" || strings.HasPrefix(p, "/canvas/") {
		p = strings.TrimPrefix(p, "/canvas")
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}
