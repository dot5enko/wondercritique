package canvas

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// fakeRenderer "renders" 1280×300 CSS px at the variant DPR: content is
// full width when the HTML says "wide", else 200 px centered; "boom" fails.
type fakeRenderer struct{ calls atomic.Int32 }

func (f *fakeRenderer) Render(_ context.Context, req render.Request) (*render.Result, error) {
	f.calls.Add(1)
	if strings.Contains(req.HTML, "boom") {
		return nil, errors.New("chrome crashed")
	}
	v := req.Variants[0].Normalize()
	w, h := v.Width, 300
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, int(float64(w)*v.DeviceScaleFactor), int(float64(h)*v.DeviceScaleFactor))))
	m := render.Metrics{ContentWidth: 200, ContentX: (w - 200) / 2, ContentHeight: h}
	if strings.Contains(req.HTML, "wide") {
		m.ContentWidth, m.ContentX = w, 0
	}
	return &render.Result{Shots: []render.Shot{{Variant: v, PNG: buf.Bytes(), Width: w, Height: h, Metrics: m}}}, nil
}

func pngSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a png: %v", err)
	}
	return cfg.Width, cfg.Height
}

func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name], _ = io.ReadAll(rc)
		_ = rc.Close()
	}
	return out
}

func TestExport(t *testing.T) {
	h := setup(t)
	h.seed(map[string]string{
		"/pricing/tiers.html":      "<div>card</div>",
		"/pricing/dark/tiers.html": "<!DOCTYPE html><html><body>dark</body></html>",
		"/hero/wide.html":          "<section>wide hero</section>",
		"/broken/boom.html":        "<div>boom</div>",
		"/broken/ok.html":          "<div>ok</div>",
	})
	mw := auth.NewTestMiddleware("0123456789abcdef0123456789abcdef", "canvas-test")
	tok := func(id string) string {
		s, err := mw.IssueAccessToken(&models.User{Base: models.Base{ID: id}, Role: "user"})
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + s
	}
	fr := &fakeRenderer{}
	newRouter := func(rd render.Renderer) *chi.Mux {
		r := chi.NewRouter()
		NewController(h.s, mw, rd).Routes(r)
		return r
	}
	r := newRouter(fr)
	url := "/threads/" + h.thread + "/canvas/export"
	do := func(r *chi.Mux, body, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", url, strings.NewReader(body))
		req.Header.Set("Authorization", tok(user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("single html fragment becomes a page", func(t *testing.T) {
		w := do(r, `{"paths":["/pricing/tiers.html"],"format":"html"}`, "user1")
		if w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename=tiers.html` ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
		}
		body := w.Body.String()
		if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "<title>tiers.html</title>") || !strings.Contains(body, "<div>card</div>") {
			t.Errorf("body = %s", body)
		}
	})

	t.Run("full documents pass through", func(t *testing.T) {
		w := do(r, `{"paths":["/pricing/dark/tiers.html"],"format":"html"}`, "user1")
		if w.Code != 200 || w.Body.String() != "<!DOCTYPE html><html><body>dark</body></html>" {
			t.Errorf("%d %s", w.Code, w.Body)
		}
	})

	t.Run("folder html → zip at canvas paths", func(t *testing.T) {
		w := do(r, `{"paths":["/pricing"],"format":"html"}`, "user1")
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != `attachment; filename=pricing.zip` {
			t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
		}
		files := unzip(t, w.Body.Bytes())
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"pricing/dark/tiers.html", "pricing/tiers.html"}) || !strings.Contains(string(files["pricing/tiers.html"]), "<div>card</div>") {
			t.Errorf("entries = %v", names)
		}
	})

	t.Run("several files → canvas.zip", func(t *testing.T) {
		w := do(r, `{"paths":["/pricing/tiers.html","/hero/wide.html"],"format":"html"}`, "user1")
		if w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename=canvas.zip` || len(unzip(t, w.Body.Bytes())) != 2 {
			t.Errorf("%d %v", w.Code, w.Header())
		}
	})

	t.Run("png 2x, narrow cropped with margins", func(t *testing.T) {
		w := do(r, `{"paths":["/pricing/tiers.html"],"format":"png"}`, "user1")
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Content-Disposition") != `attachment; filename="tiers@2x.png"` {
			t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
		}
		// 200 content + 2×48 margin = 296 < 320 min → 320 wide; 300 + 2×48 tall; ×2.
		if pw, ph := pngSize(t, w.Body.Bytes()); pw != 640 || ph != 792 {
			t.Errorf("narrow png = %dx%d", pw, ph)
		}
		w = do(r, `{"paths":["/hero/wide.html"],"format":"png"}`, "user1")
		if pw, ph := pngSize(t, w.Body.Bytes()); w.Code != 200 || pw != 2560 || ph != 600 {
			t.Errorf("wide png = %d %dx%d", w.Code, pw, ph)
		}
	})

	t.Run("png zip lists failures", func(t *testing.T) {
		w := do(r, `{"paths":["/broken"],"format":"png"}`, "user1")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		files := unzip(t, w.Body.Bytes())
		if _, ok := files["broken/ok@2x.png"]; !ok || len(files) != 2 || !strings.Contains(string(files[errorsEntry]), "/broken/boom.html") {
			t.Errorf("entries = %v", files)
		}
		if w := do(r, `{"paths":["/broken/boom.html"],"format":"png"}`, "user1"); w.Code != 500 {
			t.Errorf("single failure: %d", w.Code)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, c := range []struct {
			name, body, user string
			code             int
		}{
			{"unknown path", `{"paths":["/nope.html"],"format":"html"}`, "user1", 400},
			{"bad format", `{"paths":["/pricing"],"format":"pdf"}`, "user1", 400},
			{"no paths", `{"paths":[],"format":"html"}`, "user1", 400},
			{"other user", `{"paths":["/pricing"],"format":"html"}`, "user2", 404},
		} {
			if w := do(r, c.body, c.user); w.Code != c.code {
				t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
			}
		}
		if w := do(newRouter(nil), `{"paths":["/pricing/tiers.html"],"format":"png"}`, "user1"); w.Code != 503 {
			t.Errorf("no renderer: %d", w.Code)
		}
	})

	t.Run("png limit", func(t *testing.T) {
		many := map[string]string{}
		for i := range MaxExportPNG + 1 {
			many["/many/f"+string(rune('a'+i))+".html"] = "<div>x</div>"
		}
		h.seed(many)
		before := fr.calls.Load()
		if w := do(r, `{"paths":["/many"],"format":"png"}`, "user1"); w.Code != 400 || !strings.Contains(w.Body.String(), "at most 24") || fr.calls.Load() != before {
			t.Errorf("limit: %d %s", w.Code, w.Body)
		}
	})
}
