// The built-in style-guide agent: looks at sample images (vision) and/or
// pasted text and writes a markdown style guide that chat agents then follow
// (styleguide package).
package studio

import (
	"context"
	"encoding/json"
	"log/slog"
)

const (
	styleguideAgentName        = "Style guide generator"
	styleguideAgentDescription = "Turns sample images and/or pasted text into a markdown style guide the design chat follows."
)

const styleguideAgentSystem = `You are a senior design-systems designer. You get sample images
(screenshots, mockups, brand material), pasted text (brand guidelines, a design
system doc, CSS or design tokens, a description of the look, an existing style
guide), or both, and write a style guide in markdown that another AI will
follow when it builds HTML/CSS components. Extract the visual language that is
actually visible in the images or stated in the text — keep given values
(hex colors, fonts, sizes) exactly; when sources disagree, the text wins for
values it states. When you have to infer something (e.g. a hover state, or
everything visual when you only have a short description), say "inferred".
If the text already is a style guide, restructure it into the sections below
without dropping its rules.

Write exactly these sections, concise and concrete (values, not adjectives):

# <Name> style guide
One paragraph: the overall look and mood, who it seems made for.

## Colors
A table: role (background, surface, text, muted text, primary/accent, border,
success/warning/error when visible…), hex value, where it is used.

## Typography
Font families (name the closest Google Font and a web-safe fallback stack),
the type scale (sizes in px for display, h1–h4, body, small, caption),
weights, line heights, letter spacing, text case conventions.

## Spacing and layout
Base unit, spacing scale, typical paddings, content width, grid/columns,
alignment, density.

## Shape and depth
Corner radii per element type, border widths/styles, shadows (as CSS),
use of gradients, blur, transparency.

## Components
For each kind visible or clearly implied (buttons, inputs, cards, navigation,
badges/tags, lists, tables, modals…): anatomy, sizes, colors, states.

## Imagery and icons
Photo/illustration style, icon style (outline/filled, stroke width, size).

## Do and don't
5–10 short bullet rules that keep new components on-brand.

## Design tokens
One fenced css block with a :root { … } of CSS custom properties for the
colors, fonts, radii, shadows and spacing above.

Answer with the markdown document only: no preface, no closing remarks, no
surrounding code fence.`

func styleguideAgentSeed(modelSetID string) AgentBody {
	return AgentBody{
		ModelSetID:   modelSetID,
		SystemPrompt: styleguideAgentSystem,
		// Sources: attached images and/or pasted text (`text`); either may be
		// empty, at least one is given.
		InputTemplate:  "Write the style guide from the sources: the attached sample images (if any) and the pasted text (if any).{{images}}\n\n## Name\n\n{{name}}\n\n## Pasted text\n\n{{text}}\n\n## Notes from the user\n\n{{notes}}",
		Tools:          []string{},
		OutputContract: OutputContract{Type: "chat"},
	}
}

// ensureStyleguideAgent adds the styleguide-generator agent when it is missing
// (uses the "default" model set, skipped when that is gone). Deleting it
// re-creates it on the next start.
func ensureStyleguideAgent(ctx context.Context, store *Store, log *slog.Logger) error {
	if _, err := store.GetBySlug(ctx, KindAgent, StyleguideAgentSlug); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	ms, err := store.GetBySlug(ctx, KindModelSet, "default")
	if err != nil {
		log.Info("studio: styleguide-generator not seeded (no default model set)")
		return nil
	}
	raw, err := json.Marshal(styleguideAgentSeed(ms.ID))
	if err != nil {
		return err
	}
	def, err := store.Create(ctx, KindAgent, DefIdentity{Slug: StyleguideAgentSlug, Name: styleguideAgentName, Description: styleguideAgentDescription}, raw)
	if err != nil {
		return err
	}
	if _, _, err := store.Publish(ctx, KindAgent, def.ID, "seeded", "system", raw); err != nil {
		return err
	}
	log.Info("studio: seeded styleguide-generator agent")
	return nil
}
