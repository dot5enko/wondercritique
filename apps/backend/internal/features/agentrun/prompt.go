// Prompt assembly (plan §4.3): system prompt + auto-appended sections and the
// input message built from the sample values. Exported for tests.
package agentrun

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
)

// PromptSkill is one skill attached to an agent, for prompt assembly.
type PromptSkill struct {
	Slug     string
	Manifest string // SKILL.md content
	Files    int    // number of additional (read-only) files mounted under /skills/{slug}/
}

// placeholder matches well-formed {{name}} placeholders.
var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// promptVariableNames are the catalog prompt variables: a placeholder naming a
// known variable but missing from the samples renders as "".
var promptVariableNames = map[string]bool{
	"brief": true, "input": true, "html": true, "code": true, "images": true,
	"references": true, "critique": true, "feedback": true, "output": true,
	"criteria": true, "variation": true,
}

// AssembleSystem builds the full system prompt: the author's system prompt plus
// the auto-appended `## Rules`, `## Skills` and `## Output` sections (empty
// sections are omitted).
func AssembleSystem(system string, ruleGroups []studio.RuleGroup, skills []PromptSkill, contract studio.OutputContract) string {
	var b strings.Builder
	if strings.TrimSpace(system) != "" {
		b.WriteString(strings.TrimRight(system, "\n"))
		b.WriteString("\n")
	}

	if len(ruleGroups) > 0 {
		b.WriteString("\n## Rules\n\n")
		for _, g := range ruleGroups {
			fmt.Fprintf(&b, "### %s\n", g.Name)
			for _, r := range g.Rules {
				fmt.Fprintf(&b, "- %s\n", r.Text)
			}
			b.WriteString("\n")
		}
	}

	if len(skills) > 0 {
		b.WriteString("\n## Skills\n\n")
		for _, s := range skills {
			fmt.Fprintf(&b, "### %s\n\n%s\n", s.Slug, strings.TrimRight(s.Manifest, "\n"))
			if s.Files > 0 {
				fmt.Fprintf(&b, "\n%d additional file(s) of this skill are mounted read-only at /skills/%s/.\n", s.Files, s.Slug)
			}
			b.WriteString("\n")
		}
	}

	if out := outputSection(contract); out != "" {
		b.WriteString("\n" + out)
	}
	return strings.TrimRight(b.String(), "\n")
}

// outputSection renders the `## Output` contract instructions.
func outputSection(contract studio.OutputContract) string {
	switch contract.Type {
	case "artifact":
		return "## Output\n\nProduce one complete HTML component fragment as your answer and call " +
			"submit_artifact with it to finish. The fragment must pass the platform rules " +
			"(no scripts, one root element, scoped CSS)."
	case "critique":
		return "## Output\n\nRespond by calling submit_critique with a JSON object " +
			`{"strengths":[...], "issues":[{"severity","code"?,"message","suggestion"?}], "score": 0-10}. ` +
			"Issue severity is info, warning or error; the score summarizes the overall quality."
	case "chat":
		return "## Output\n\nAnswer the user in plain text (markdown). There is no submit tool: your last " +
			"message is the reply. Components exist only when you create them with gen_components; " +
			"never paste HTML into the reply instead."
	case "custom":
		var b strings.Builder
		b.WriteString("## Output\n\nRespond by calling submit_fields with the following fields:\n\n")
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(contract.FieldsSchema, &schema); err == nil {
			required := map[string]bool{}
			for _, r := range schema.Required {
				required[r] = true
			}
			names := make([]string, 0, len(schema.Properties))
			for name := range schema.Properties {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				var fs struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				}
				_ = json.Unmarshal(schema.Properties[name], &fs)
				req := ""
				if required[name] {
					req = ", required"
				}
				desc := ""
				if fs.Description != "" {
					desc = ": " + fs.Description
				}
				fmt.Fprintf(&b, "- %s (%s%s)%s\n", name, fs.Type, req, desc)
			}
		}
		b.WriteString("\nCall submit_fields with a JSON object of these fields to finish.\n")
		return strings.TrimRight(b.String(), "\n")
	}
	return ""
}

// isImageValue reports whether a sample value is an image reference (a data:
// URL or a wcblob:// link) rather than text.
func isImageValue(v string) bool {
	return strings.HasPrefix(v, "data:image/") || strings.HasPrefix(v, "wcblob://")
}

// imageValues returns the image references held by a sample value: a single
// image string, or a non-empty list ([]string / []any) made only of images.
// nil means the value is not an image (it renders as text).
func imageValues(v any) []string {
	switch x := v.(type) {
	case string:
		if isImageValue(x) {
			return []string{x}
		}
	case []string:
		if len(x) == 0 {
			return nil
		}
		for _, s := range x {
			if !isImageValue(s) {
				return nil
			}
		}
		return x
	case []any:
		if len(x) == 0 {
			return nil
		}
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok || !isImageValue(s) {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// BuildInput builds the user input message from the input template and the
// sample map. {{name}} placeholders substitute the matching sample value
// (missing known variables render as ""; unknown placeholders stay verbatim);
// an empty template renders one `## {Key}` markdown section per sample entry.
// Image values become llm.ImageBlocks after the text; in preview they render
// as `[image: {name}]` and no blocks are returned.
func BuildInput(template string, samples map[string]any, preview bool) llm.Message {
	render := func(v any) string {
		switch x := v.(type) {
		case string:
			if isImageValue(x) {
				return "" // handled as an image
			}
			return x
		case nil:
			return ""
		default:
			raw, err := json.Marshal(x)
			if err != nil {
				return fmt.Sprintf("%v", x)
			}
			return string(raw)
		}
	}

	var text string
	if strings.TrimSpace(template) != "" {
		text = placeholder.ReplaceAllStringFunc(template, func(m string) string {
			name := placeholder.FindStringSubmatch(m)[1]
			v, ok := samples[name]
			if !ok {
				if promptVariableNames[name] {
					return "" // known variable, missing sample
				}
				return m // unknown placeholder stays verbatim
			}
			if imgs := imageValues(v); imgs != nil {
				if preview {
					return "[image: " + name + "]"
				}
				return "" // the images become blocks after the text
			}
			return render(v)
		})
	} else {
		keys := make([]string, 0, len(samples))
		for k := range samples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			v := samples[k]
			if imageValues(v) != nil {
				if preview {
					fmt.Fprintf(&sb, "## %s\n\n[image: %s]\n\n", k, k)
				}
				continue // real image: block below
			}
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", k, render(v))
		}
		text = strings.TrimRight(sb.String(), "\n")
	}

	blocks := []llm.Block{}
	if text != "" {
		blocks = append(blocks, llm.TextBlock(text))
	}
	if !preview {
		// Images after the text, in a stable (sorted) order.
		keys := make([]string, 0, len(samples))
		for k := range samples {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, img := range imageValues(samples[k]) {
				blocks = append(blocks, llm.ImageBlock(img))
			}
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, llm.TextBlock(""))
	}
	return llm.NewMessage(llm.RoleUser, blocks...)
}
