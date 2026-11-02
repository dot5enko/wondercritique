// Built-in defaults seeding (plan §8): inserted once when studio_defs is empty.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// SeedDefaults inserts the built-in definitions when the studio_defs table is
// empty. Idempotent by emptiness; safe to call on every start.
func SeedDefaults(ctx context.Context, store *Store, log *slog.Logger) error {
	var count int64
	for _, kind := range Kinds {
		defs, err := store.List(ctx, kind)
		if err != nil {
			return fmt.Errorf("studio: count defs: %w", err)
		}
		count += int64(len(defs))
	}
	if count > 0 {
		return nil
	}

	seeded := 0
	seed := func(kind, slug, name, description string, body any) error {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		def, err := store.Create(ctx, kind, DefIdentity{Slug: slug, Name: name, Description: description}, raw)
		if err != nil {
			return err
		}
		_, _, err = store.Publish(ctx, kind, def.ID, "seeded", "system", raw)
		return err
	}

	// model_set: default (single muse-spark-1.3-contributor).
	if err := seed(KindModelSet, "default", "Default model set",
		"Single Meta muse-spark-1.3-contributor model.",
		ModelSetBody{
			Strategy: "single",
			Entries: []ModelSetEntry{{
				ProviderID: "meta",
				ModelID:    "muse-spark-1.3-contributor",
			}},
		}); err != nil {
		return err
	}
	defaultSet, err := store.GetBySlug(ctx, KindModelSet, "default")
	if err != nil {
		return err
	}
	seeded++

	// rules: component-format groups covering the validator codes.
	if err := seed(KindRule, "component-format", "Component format rules",
		"Structural and CSS rules for generated HTML components, aligned with the component validator.",
		RuleBody{Groups: []RuleGroup{
			{Name: "Cleanups", Rules: []RuleItem{
				{Text: "Remove unused CSS selectors and classes before submitting.", Severity: "warning"},
				{Text: "Inline small SVGs as markup instead of external files.", Severity: "info"},
			}},
			{Name: "Structure", Rules: []RuleItem{
				{Text: "Send only the component fragment: no doctype, html, head, body or script tags.", Severity: "error", ValidatorCode: "no-document-wrapper"},
				{Text: "Exactly one root element wraps the whole component.", Severity: "error", ValidatorCode: "single-root"},
				{Text: "Never use iframes, frames or embedded objects.", Severity: "error", ValidatorCode: "no-iframe"},
				{Text: "Never include JavaScript: no script tags, no inline handlers.", Severity: "error", ValidatorCode: "no-script"},
				{Text: "The component must fit the stage height without horizontal overflow.", Severity: "error", ValidatorCode: "stage-height"},
			}},
			{Name: "CSS", Rules: []RuleItem{
				{Text: "Scope every CSS rule to a class or id present in the markup.", Severity: "error", ValidatorCode: "scoped-css"},
				{Text: "Use at most one <style> block and no @import except Google Fonts.", Severity: "error", ValidatorCode: "style-block"},
				{Text: "Do not target html or body, and do not use unscoped universal resets.", Severity: "error", ValidatorCode: "no-root-selectors"},
				{Text: "Use external resources only as data: URIs or Google Fonts links.", Severity: "warning", ValidatorCode: "external-resource"},
			}},
		}}); err != nil {
		return err
	}
	seeded++

	// skills: none by default (attached skills are user content).

	// agents. All use the seeded default model set.
	msID := defaultSet.ID
	type agentSeed struct {
		slug, name, description, system, input string
		contract                               string
		tools                                  []string
	}
	generatorSystem := `You are a senior front-end component engineer.
You produce one self-contained HTML component fragment per request.

Rules you must follow:
- Send only the component fragment (no <html>, <head>, <body>, <script>).
- Exactly one root element; keep it small, semantic and accessible.
- All CSS lives in at most one <style> block, scoped to your own classes.
- External resources only as data: URIs or Google Fonts links.

Work iteratively: inspect the feedback and screenshots you receive, fix the
issues, and call submit_artifact with the complete HTML fragment when done.`
	visionCriticSystem := `You are a meticulous UI design critic. You receive a rendered screenshot
of an HTML component plus its layout metrics and the original brief.

Judge visual quality: layout, spacing, hierarchy, typography, color, contrast,
responsiveness and polish. Call submit_critique with your findings:
- strengths: what works well
- issues: concrete, actionable problems with severity, a short code, a message
  and a suggestion
- score: an overall 0-10 quality score`
	codeCriticSystem := `You are a strict front-end code reviewer. You receive an HTML component
fragment (and optionally the validator report).

Judge the code: semantic markup, accessibility, CSS scoping and quality,
resource usage, and adherence to the platform rules. Call submit_critique with
strengths, issues (severity, code, message, suggestion) and a 0-10 score.`
	criticSystem := `You are Wonder Critique, a helpful design critic. Discuss the work with the
user, answer questions, and give actionable UI feedback. Call submit_critique
with strengths, issues and a 0-10 score when you deliver a verdict.`
	agents := []agentSeed{
		{slug: "component-generator", name: "Component generator",
			description: "Generates and iterates on an HTML component; needs file and sandbox tools.",
			system:      generatorSystem,
			input:       "## Brief\n\n{{brief}}\n\n{{feedback}}",
			contract:    "artifact",
			tools:       []string{"read_file", "write_file", "edit_file", "glob", "grep", "run_js", "validate_component"},
		},
		{slug: "vision-critic", name: "Vision critic",
			description: "Criticizes rendered screenshots (needs a vision-capable model); submits a structured critique.",
			system:      visionCriticSystem,
			input:       "## Brief\n\n{{brief}}\n\n## Screenshots\n\n{{images}}\n\n## Previous critique\n\n{{feedback}}",
			contract:    "critique",
		},
		{slug: "code-critic", name: "Code critic",
			description: "Reviews the component code and validator report; submits a structured critique.",
			system:      codeCriticSystem,
			input:       "## Brief\n\n{{brief}}\n\n## Current file\n\n{{html}}\n\n## Previous critique\n\n{{feedback}}",
			contract:    "critique",
		},
		{slug: "critic", name: "Critic",
			description: "The chat critic: discuss design and deliver structured verdicts.",
			system:      criticSystem,
			input:       "{{brief}}",
			contract:    "critique",
		},
	}
	agentIDs := map[string]string{}
	for _, a := range agents {
		if err := seed(KindAgent, a.slug, a.name, a.description, AgentBody{
			ModelSetID:     msID,
			SystemPrompt:   a.system,
			InputTemplate:  a.input,
			Tools:          a.tools,
			OutputContract: OutputContract{Type: a.contract},
		}); err != nil {
			return err
		}
		def, err := store.GetBySlug(ctx, KindAgent, a.slug)
		if err != nil {
			return err
		}
		agentIDs[a.slug] = def.ID
		seeded++
	}

	// pipeline: the design-loop graph (plan §4.5).
	if err := seed(KindPipeline, "default", "Design loop",
		"Input → generator → render → vision + code critique → merge → gate loop → output.",
		PipelineBody{
			Nodes: []PipelineNode{
				{ID: "input", Type: "input", Config: map[string]any{"label": "Input"}, Position: NodePosition{X: 0, Y: 200}},
				{ID: "generator", Type: "agent", Config: map[string]any{"label": "Generator", "agent_id": agentIDs["component-generator"]}, Position: NodePosition{X: 260, Y: 200}},
				{ID: "render", Type: "render", Config: map[string]any{"label": "Render"}, Position: NodePosition{X: 520, Y: 200}},
				{ID: "vision-critic", Type: "agent", Config: map[string]any{"label": "Vision critic", "agent_id": agentIDs["vision-critic"]}, Position: NodePosition{X: 780, Y: 80}},
				{ID: "code-critic", Type: "agent", Config: map[string]any{"label": "Code critic", "agent_id": agentIDs["code-critic"]}, Position: NodePosition{X: 780, Y: 320}},
				{ID: "merge", Type: "merge", Config: map[string]any{"label": "Merge", "dedupe": true}, Position: NodePosition{X: 1040, Y: 200}},
				{ID: "gate", Type: "gate", Config: map[string]any{"label": "Gate", "max_iterations": 3, "patience": 1}, Position: NodePosition{X: 1300, Y: 200}},
				{ID: "output", Type: "output", Config: map[string]any{"label": "Output", "strategy": "best"}, Position: NodePosition{X: 1560, Y: 200}},
			},
			Edges: []PipelineEdge{
				{ID: "input:brief->generator:brief", From: EdgeEndpoint{Node: "input", Port: "brief"}, To: EdgeEndpoint{Node: "generator", Port: "brief"}},
				{ID: "generator:html->render:html", From: EdgeEndpoint{Node: "generator", Port: "html"}, To: EdgeEndpoint{Node: "render", Port: "html"}},
				{ID: "render:images->vision-critic:images", From: EdgeEndpoint{Node: "render", Port: "images"}, To: EdgeEndpoint{Node: "vision-critic", Port: "images"}},
				{ID: "generator:html->code-critic:html", From: EdgeEndpoint{Node: "generator", Port: "html"}, To: EdgeEndpoint{Node: "code-critic", Port: "html"}},
				{ID: "vision-critic:critique->merge:critique", From: EdgeEndpoint{Node: "vision-critic", Port: "critique"}, To: EdgeEndpoint{Node: "merge", Port: "critique"}},
				{ID: "code-critic:critique->merge:critique", From: EdgeEndpoint{Node: "code-critic", Port: "critique"}, To: EdgeEndpoint{Node: "merge", Port: "critique"}},
				{ID: "merge:critique->gate:critique", From: EdgeEndpoint{Node: "merge", Port: "critique"}, To: EdgeEndpoint{Node: "gate", Port: "critique"}},
				{ID: "gate:continue->generator:feedback", From: EdgeEndpoint{Node: "gate", Port: "continue"}, To: EdgeEndpoint{Node: "generator", Port: "feedback"}, Loop: true},
				{ID: "gate:done->output:html", From: EdgeEndpoint{Node: "gate", Port: "done"}, To: EdgeEndpoint{Node: "output", Port: "html"}},
				{ID: "merge:critique->output:critique", From: EdgeEndpoint{Node: "merge", Port: "critique"}, To: EdgeEndpoint{Node: "output", Port: "critique"}},
			},
		}); err != nil {
		return err
	}
	seeded++

	log.Info("studio: seeded built-in definitions", "count", seeded)
	return nil
}
