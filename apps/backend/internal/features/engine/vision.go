package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

const (
	// maxVisionShots is how many selected components a vision message may show.
	maxVisionShots = 6
	// visionDPR: 1x keeps the images small for the model (the canvas width is 1280).
	visionDPR = 1
	// visionSample is the prompt variable holding the screenshots (image blocks
	// after the message text).
	visionSample = "previews"
)

// visionSamples renders the selected components of a vision message (in
// selection order), records the screenshots as image artifacts of the step
// and hands them to the agent: the images as the `previews` variable and a
// <meta> note in front of the brief telling it what they are. Files that fail
// to render are named in the note; all of them failing fails the step.
func (nr *nodeRun) visionSamples(ctx context.Context, samples map[string]any) error {
	sel := nr.st.canvas.selected
	if len(sel) == 0 {
		return nil
	}
	var (
		refs, shown, failed []string
		first               error
	)
	for i, f := range sel {
		nr.activity(fmt.Sprintf("Rendering preview %d/%d", i+1, len(sel)))
		png, err := nr.screenshot(ctx, f)
		if err == nil {
			var a *models.Artifact
			if a, err = nr.artifact(ctx, models.ArtifactImage, png, "image/png", nil); err == nil {
				refs = append(refs, blobs.URL(a.BlobSHA))
				shown = append(shown, f.Path)
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		first = firstErr(first, err)
		failed = append(failed, fmt.Sprintf("%s (%s)", f.Path, errText(err)))
	}
	if len(refs) == 0 {
		return fmt.Errorf("could not render a preview: %s", errText(first))
	}
	samples[visionSample] = refs
	brief, _ := samples["brief"].(string)
	samples["brief"] = visionMeta(shown, failed) + "\n\n" + brief
	return nil
}

func (nr *nodeRun) screenshot(ctx context.Context, f models.CanvasFile) ([]byte, error) {
	src, err := nr.st.e.Canvas.Content(ctx, f)
	if err != nil {
		return nil, err
	}
	return canvas.Screenshot(ctx, nr.st.e.Renderer, string(src), visionDPR)
}

// visionMeta tells the agent what the attached images are.
func visionMeta(shown, failed []string) string {
	var b strings.Builder
	b.WriteString("<meta>Vision mode: the attached image")
	if len(shown) > 1 {
		b.WriteString("s are rendered screenshots of the selected components, in this order: ")
	} else {
		b.WriteString(" is a rendered screenshot of the selected component ")
	}
	b.WriteString(strings.Join(shown, ", "))
	b.WriteString(". Look at ")
	if len(shown) > 1 {
		b.WriteString("them")
	} else {
		b.WriteString("it")
	}
	b.WriteString(" carefully and try to fulfill the user's request below, judging by what is actually rendered (not only the code).")
	if len(failed) > 0 {
		b.WriteString(" These could not be rendered: " + strings.Join(failed, "; ") + ".")
	}
	b.WriteString("</meta>")
	return b.String()
}

func firstErr(first, err error) error {
	if first == nil {
		return err
	}
	return first
}

// errText is the user-facing text of a render failure.
func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}
