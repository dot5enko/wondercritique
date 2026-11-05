// Style guides of chat threads: the guide a session follows (session metadata
// styleguide_id) is appended to every model request's system prompt and
// mounted read-only at /styleguide/STYLEGUIDE.md.
package agentrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

const (
	// StyleguideDir is where a session's style guide is mounted.
	StyleguideDir = "/styleguide"
	// StyleguideFile is the guide's file name inside StyleguideDir.
	StyleguideFile = "STYLEGUIDE.md"
	// maxStyleguidePrompt caps the guide's share of the system prompt.
	maxStyleguidePrompt = 40_000
)

// styleguideMarkdown loads a style guide's markdown by id. Deleted guides
// still resolve (threads that picked one keep it); unknown ids give "".
func (r *Runtime) styleguideMarkdown(ctx context.Context, id string) (string, error) {
	if r.gdb == nil {
		return "", nil
	}
	var sg models.Styleguide
	err := r.gdb.WithContext(ctx).Unscoped().Select("markdown").First(&sg, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("agentrun: load style guide: %w", err)
	}
	return strings.TrimSpace(sg.Markdown), nil
}

// StyleguideSystem appends the style guide section to a system prompt
// (unchanged when md is empty).
func StyleguideSystem(system, md string) string {
	if md == "" {
		return system
	}
	if len(md) > maxStyleguidePrompt {
		md = md[:maxStyleguidePrompt] + "\n\n[… cut; read the full guide at " + StyleguideDir + "/" + StyleguideFile + "]"
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(system, "\n"))
	b.WriteString("\n\n## Style guide\n\n")
	b.WriteString("This chat follows the style guide below (also at " + StyleguideDir + "/" + StyleguideFile + "). ")
	b.WriteString("Every component must follow it: colors, typography, spacing, shapes, component styles and tokens. ")
	b.WriteString("Only deviate where the user explicitly asks for something else.\n\n")
	b.WriteString("<styleguide>\n")
	b.WriteString(md)
	b.WriteString("\n</styleguide>")
	return b.String()
}
