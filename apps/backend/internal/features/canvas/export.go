package canvas

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
)

// Export formats.
const (
	FormatHTML = "html"
	FormatPNG  = "png"
)

// Export limits and the PNG capture. Screenshots are slow (Chrome, shared with
// the engine), so a PNG zip is capped well below the request timeout.
const (
	MaxExportHTML = 200
	MaxExportPNG  = 24

	exportWidth       = 1280 // stage width, like the canvas frames
	exportMargin      = 48   // CSS px around narrow components (cards, buttons, …)
	exportMinWidth    = 320  // narrowest cropped PNG, CSS px
	exportNarrow      = 0.75 // content narrower than this share of the stage gets cropped
	exportConcurrency = 2
	errorsEntry       = "export-errors.txt"
)

type exportRequest struct {
	Paths  []string `json:"paths" validate:"required,min=1,max=200"`
	Format string   `json:"format" validate:"required,oneof=html png"`
}

// export serves POST /threads/{id}/canvas/export: one selected file → that
// file; folders or several paths → a zip keeping the canvas paths.
func (c *Controller) export(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[exportRequest](r)
	if err != nil {
		return err
	}
	id, _, err := c.thread(r)
	if err != nil {
		return err
	}
	files, err := c.store.Resolve(r.Context(), id, req.Paths, "paths")
	if err != nil {
		return err
	}
	limit := MaxExportHTML
	if req.Format == FormatPNG {
		limit = MaxExportPNG
	}
	if len(files) > limit {
		return apperr.Invalid("invalid input", map[string]string{
			"paths": fmt.Sprintf("at most %d components can be exported as %s at once (%d selected)", limit, req.Format, len(files)),
		})
	}
	ex := exporter{store: c.store, renderer: c.renderer, format: req.Format}

	if len(req.Paths) == 1 && strings.HasSuffix(req.Paths[0], fileExt) && len(files) == 1 {
		data, err := ex.file(r.Context(), files[0])
		if err != nil {
			return err
		}
		return download(w, ex.name(files[0].Path), ex.mime(), data)
	}
	data, err := ex.zip(r.Context(), files)
	if err != nil {
		return err
	}
	return download(w, zipName(req.Paths), "application/zip", data)
}

func download(w http.ResponseWriter, name, mimeType string, data []byte) error {
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("Content-Length", fmt.Sprint(len(data)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(data)
	return err
}

// zipName: one folder → `<folder>.zip`, anything else → `canvas.zip`.
func zipName(paths []string) string {
	if len(paths) == 1 {
		if p, err := CleanFolder(paths[0]); err == nil && p != "/" {
			return path.Base(p) + ".zip"
		}
	}
	return "canvas.zip"
}

type exporter struct {
	store    *Store
	renderer render.Renderer
	format   string
}

func (e exporter) mime() string {
	if e.format == FormatPNG {
		return "image/png"
	}
	return "text/html; charset=utf-8"
}

// name of a file's download: `tiers.html` / `tiers@2x.png`.
func (e exporter) name(p string) string {
	base := path.Base(p)
	if e.format == FormatPNG {
		return strings.TrimSuffix(base, fileExt) + "@2x.png"
	}
	return base
}

// file exports one canvas file.
func (e exporter) file(ctx context.Context, f models.CanvasFile) ([]byte, error) {
	src, err := e.store.Content(ctx, f)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if e.format == FormatHTML {
		return htmlDocument(path.Base(f.Path), src), nil
	}
	return e.screenshot(ctx, string(src))
}

// zip exports files into an archive at their canvas paths. A file that fails
// to render is listed in export-errors.txt; all of them failing is an error.
func (e exporter) zip(ctx context.Context, files []models.CanvasFile) ([]byte, error) {
	out := make([][]byte, len(files))
	errs := make([]error, len(files))
	sem := make(chan struct{}, exportConcurrency)
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = e.file(ctx, f)
		})
	}
	wg.Wait()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var failures []string
	var firstErr error
	for i, f := range files {
		if errs[i] != nil {
			firstErr = cmpErr(firstErr, errs[i])
			failures = append(failures, fmt.Sprintf("%s: %s", f.Path, errorText(errs[i])))
			continue
		}
		name := path.Join(path.Dir(strings.TrimPrefix(f.Path, "/")), e.name(f.Path))
		if err := writeEntry(zw, name, f.CreatedAt, out[i], e.format == FormatHTML); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	if len(failures) == len(files) {
		return nil, firstErr
	}
	if len(failures) > 0 {
		text := "These components could not be exported:\n\n" + strings.Join(failures, "\n") + "\n"
		if err := writeEntry(zw, errorsEntry, time.Now(), []byte(text), true); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, apperr.Internal(err)
	}
	return buf.Bytes(), nil
}

func cmpErr(first, err error) error {
	if first == nil {
		return err
	}
	return first
}

func errorText(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

// writeEntry adds one file; PNGs are stored (already compressed), text deflated.
func writeEntry(zw *zip.Writer, name string, mod time.Time, data []byte, deflate bool) error {
	method := zip.Store
	if deflate {
		method = zip.Deflate
	}
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: mod})
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

/* ---------- HTML ---------- */

// htmlDocument makes a fragment a standalone page (the canvas shows it the
// same way: as the whole document of a frame). Full documents pass through.
func htmlDocument(title string, src []byte) []byte {
	head := strings.ToLower(string(bytes.TrimSpace(src[:min(len(src), 1024)])))
	if strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") {
		return src
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(title) + "</title>\n</head>\n<body>\n")
	b.Write(bytes.TrimSpace(src))
	b.WriteString("\n</body>\n</html>\n")
	return b.Bytes()
}

/* ---------- PNG ---------- */

// exportVariant: light, white, as tall as the content allows, at `dpr`.
func exportVariant(dpr float64) render.Variant {
	return render.Variant{
		Name: "export", Width: exportWidth, StageHeight: 40, ColorScheme: "light",
		Background: "#ffffff", DeviceScaleFactor: dpr, MaxHeight: 4800,
	}
}

func (e exporter) screenshot(ctx context.Context, src string) ([]byte, error) {
	return Screenshot(ctx, e.renderer, src, 2)
}

// Screenshot renders a component the way the canvas shows it (1280 px stage,
// light, narrow content cropped with a margin) as a PNG at `dpr`. Used by the
// PNG export (2x) and by vision-mode chat messages (the engine).
func Screenshot(ctx context.Context, renderer render.Renderer, src string, dpr float64) ([]byte, error) {
	if renderer == nil {
		return nil, apperr.Unavailable("screenshots are not available on this server")
	}
	res, err := renderer.Render(ctx, render.Request{HTML: src, Variants: []render.Variant{exportVariant(dpr)}})
	switch {
	case errors.Is(err, render.ErrNoBrowser):
		return nil, apperr.Unavailable("screenshots are not available on this server").Wrap(err)
	case errors.Is(err, render.ErrInvalidRequest):
		return nil, apperr.Invalid("this component can't be rendered", nil).Wrap(err)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, apperr.Unavailable("rendering took too long").Wrap(err)
	case err != nil:
		return nil, apperr.Internal(err)
	case len(res.Shots) == 0:
		return nil, apperr.Internal(errors.New("canvas: render returned no shots"))
	}
	out, err := cropShot(res.Shots[0])
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return out, nil
}

// cropShot trims a narrow component (content < 75% of the stage) to its
// content plus a margin on every side; wide ones keep the full stage.
func cropShot(s render.Shot) ([]byte, error) {
	m := s.Metrics
	if m.ContentWidth <= 0 || float64(m.ContentWidth) >= exportNarrow*float64(s.Width) {
		return s.PNG, nil
	}
	left := max(0, m.ContentX-exportMargin)
	right := min(s.Width, m.ContentX+m.ContentWidth+exportMargin)
	if grow := exportMinWidth - (right - left); grow > 0 {
		left = max(0, left-grow/2)
		right = min(s.Width, left+exportMinWidth)
	}
	img, err := png.Decode(bytes.NewReader(s.PNG))
	if err != nil {
		return nil, fmt.Errorf("canvas: decode shot: %w", err)
	}
	dpr := s.Variant.DeviceScaleFactor
	px := func(v int) int { return int(float64(v) * dpr) }
	b := img.Bounds()
	src := image.Rect(b.Min.X+px(left), b.Min.Y, min(b.Max.X, b.Min.X+px(right)), b.Max.Y)
	pad := px(exportMargin)
	dst := image.NewRGBA(image.Rect(0, 0, src.Dx(), src.Dy()+2*pad))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(0, pad, src.Dx(), pad+src.Dy()), img, src.Min, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("canvas: encode shot: %w", err)
	}
	return buf.Bytes(), nil
}
