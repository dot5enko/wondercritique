package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// testChrome builds a renderer that skips the test when no Chrome is found.
func testChrome(t *testing.T, cfg Config) *Chrome {
	t.Helper()
	if _, err := findChrome(cfg.ChromePath); err != nil {
		t.Skipf("skipping: %v", err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func pngSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return cfg.Width, cfg.Height
}

const guideFragment = `<div class="card">
  <h1>Wonder Card</h1>
  <p>Body copy for the contrast check.</p>
  <style>
    .card { width: 320px; padding: 16px; background: #ffffff; color: #111111;
            border-radius: 8px; font-family: system-ui, sans-serif; }
    .card h1 { font-size: 20px; margin: 0 0 8px; }
    .card p { font-size: 14px; margin: 0; color: #555555; }
    @media (prefers-color-scheme: dark) {
      .card { background: #1c1d21; color: #f2f2f2; }
      .card p { color: #b5b5b5; }
    }
  </style>
</div>`

// TestRenderGuideLightDark is the reference render: one fragment in the
// standard light + dark pair, checking PNG geometry, metrics plausibility and
// prefers-color-scheme emulation. It needs no external network.
func TestRenderGuideLightDark(t *testing.T) {
	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := c.Render(ctx, Request{HTML: guideFragment, Variants: LightDark()})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if res.CanvasVersion != CanvasVersion {
		t.Fatalf("canvas version = %q", res.CanvasVersion)
	}
	if len(res.Shots) != 2 {
		t.Fatalf("want 2 shots, got %d", len(res.Shots))
	}

	for _, shot := range res.Shots {
		w, h := pngSize(t, shot.PNG)
		if want := shot.Width * int(shot.Variant.DeviceScaleFactor); w != want {
			t.Errorf("%s: png width = %d, want %d (css×dpr)", shot.Variant.Name, w, want)
		}
		if want := shot.Height * int(shot.Variant.DeviceScaleFactor); h != want {
			t.Errorf("%s: png height = %d, want %d (css×dpr)", shot.Variant.Name, h, want)
		}
		if shot.Height < 220 || shot.Height > 1200 {
			t.Errorf("%s: capture height %d outside [220, 1200]", shot.Variant.Name, shot.Height)
		}

		m := shot.Metrics
		if m.Stage.Width != 480 {
			t.Errorf("%s: stage width = %d", shot.Variant.Name, m.Stage.Width)
		}
		if m.TopLevelElementCount != 1 {
			t.Errorf("%s: top-level elements = %d, want 1", shot.Variant.Name, m.TopLevelElementCount)
		}
		if m.Root.Width < 300 || m.Root.Height < 50 {
			t.Errorf("%s: root box implausible: %+v", shot.Variant.Name, m.Root)
		}
		if m.Root.X < 0 || m.Root.X+m.Root.Width > m.Stage.Width+1 {
			t.Errorf("%s: root escapes stage horizontally: %+v", shot.Variant.Name, m.Root)
		}
		if m.ContentHeight < m.Root.Height {
			t.Errorf("%s: content height %d < root height %d", shot.Variant.Name, m.ContentHeight, m.Root.Height)
		}
		if m.OverflowX || m.OverflowY {
			t.Errorf("%s: unexpected overflow x=%v y=%v", shot.Variant.Name, m.OverflowX, m.OverflowY)
		}
		if len(m.BlockedRequests) != 0 || len(m.FailedRequests) != 0 {
			t.Errorf("%s: blocked=%+v failed=%+v, want none for a self-contained fragment",
				shot.Variant.Name, m.BlockedRequests, m.FailedRequests)
		}
		if len(m.TextRuns) == 0 {
			t.Errorf("%s: no text runs", shot.Variant.Name)
		}
		var withContrast int
		for _, tr := range m.TextRuns {
			if tr.Contrast >= 1 {
				withContrast++
				if tr.FontSize == "" || tr.FontFamily == "" || tr.FontWeight == "" {
					t.Errorf("%s: run missing font metrics: %+v", shot.Variant.Name, tr)
				}
			}
		}
		if withContrast == 0 {
			t.Errorf("%s: no run with computable contrast: %+v", shot.Variant.Name, m.TextRuns)
		}
	}

	// Color-scheme emulation: the card background must differ between shots.
	bgOf := func(shot Shot) string {
		for _, tr := range shot.Metrics.TextRuns {
			if tr.Text == "Wonder Card" {
				return tr.Background
			}
		}
		t.Fatalf("title run not found in %+v", shot.Metrics.TextRuns)
		return ""
	}
	lightBG, darkBG := bgOf(res.Shots[0]), bgOf(res.Shots[1])
	if lightBG != "rgb(255, 255, 255)" {
		t.Errorf("light card background = %q", lightBG)
	}
	if darkBG != "rgb(28, 29, 33)" {
		t.Errorf("dark card background = %q", darkBG)
	}

	// The canvas background must differ too (corner pixel outside the card).
	pixel := func(data []byte) uint32 {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, _ := img.At(4, 4).RGBA()
		return r>>8<<16 | g>>8<<8 | b>>8
	}
	if pixel(res.Shots[0].PNG) == pixel(res.Shots[1].PNG) {
		t.Error("light and dark canvas backgrounds are identical")
	}
}

// TestRenderOverTall checks content growth past the stage and the capture cap.
func TestRenderOverTall(t *testing.T) {
	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("400px content", func(t *testing.T) {
		frag := `<div style="width:200px;height:400px;background:#3366ff;color:#fff;font:16px sans-serif">Tall</div>`
		res, err := c.Render(ctx, Request{HTML: frag})
		if err != nil {
			t.Fatal(err)
		}
		shot := res.Shots[0]
		if shot.Metrics.ContentHeight < 395 {
			t.Errorf("content height = %d, want ≈400 (> stage 220)", shot.Metrics.ContentHeight)
		}
		if shot.Metrics.StageHeight != shot.Metrics.ContentHeight {
			t.Errorf("stage height %d should have grown with content %d",
				shot.Metrics.StageHeight, shot.Metrics.ContentHeight)
		}
		if shot.Height < 400 || shot.Height > 410 {
			t.Errorf("capture height = %d, want ≈400", shot.Height)
		}
		w, h := pngSize(t, shot.PNG)
		if w != 960 || h != shot.Height*2 {
			t.Errorf("png = %dx%d, want 960x%d", w, h, shot.Height*2)
		}
	})

	t.Run("capped at max height", func(t *testing.T) {
		frag := `<div style="width:200px;height:1500px;background:#3366ff;color:#fff;font:16px sans-serif">Very tall</div>`
		res, err := c.Render(ctx, Request{HTML: frag})
		if err != nil {
			t.Fatal(err)
		}
		shot := res.Shots[0]
		if shot.Metrics.ContentHeight < 1490 {
			t.Errorf("content height = %d, want ≈1500", shot.Metrics.ContentHeight)
		}
		if shot.Height != 1200 {
			t.Errorf("capture height = %d, want the 1200 cap", shot.Height)
		}
		if _, h := pngSize(t, shot.PNG); h != 2400 {
			t.Errorf("png height = %d, want 2400", h)
		}
	})
}

// TestRenderScriptHasNoEffect proves both script defenses: the fragment's
// <script> and <img onerror> must not run (page JS disabled + CSP), so the
// document title stays renderer-owned.
func TestRenderScriptHasNoEffect(t *testing.T) {
	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frag := `<div id="root" style="height:50px;background:#ffffff;color:#111111;font:14px sans-serif">
<script>document.getElementById('root').style.height='9999px'</script>
<img src="https://evil.example/x.png" alt="x" onerror="document.getElementById('root').style.height='9999px'">
<span>Still fine</span>
</div>`
	res, err := c.Render(ctx, Request{HTML: frag})
	if err != nil {
		t.Fatal(err)
	}
	shot := res.Shots[0]

	// Neither the <script> nor the onerror handler may have run: both would
	// have inflated the root to 9999px.
	if shot.Metrics.ContentHeight >= 500 {
		t.Errorf("script/handler had an effect: content height = %d", shot.Metrics.ContentHeight)
	}
	if shot.Metrics.OverflowY || shot.Metrics.OverflowX {
		t.Errorf("script/handler had an effect: overflow x=%v y=%v",
			shot.Metrics.OverflowX, shot.Metrics.OverflowY)
	}

	// Blocked image request recorded (CSP or the fetch filter).
	var blocked bool
	for _, b := range shot.Metrics.BlockedRequests {
		if strings.Contains(b.URL, "evil.example") {
			blocked = true
			if b.Reason == "" {
				t.Errorf("blocked entry without reason: %+v", b)
			}
		}
	}
	if !blocked {
		t.Errorf("evil.example request not recorded as blocked: %+v", shot.Metrics.BlockedRequests)
	}
	// The visible text still rendered (nothing crashed the layout).
	found := false
	for _, tr := range shot.Metrics.TextRuns {
		if strings.Contains(tr.Text, "Still fine") {
			found = true
		}
	}
	if !found {
		t.Errorf("text runs lost: %+v", shot.Metrics.TextRuns)
	}
}

// TestRenderBlocksExternalRequests proves nothing in the fragment can reach
// the network: img, link, CSS url(), @import and video poster referencing a
// local server get zero hits, and the cloud-metadata address is blocked.
// Local-only: no external network needed.
func TestRenderBlocksExternalRequests(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/css")
		_, _ = io.WriteString(w, "body{color:red}")
	}))
	defer srv.Close()

	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frag := fmt.Sprintf(`<div id="root">
<img src="%s/img.png" alt="i">
<link rel="stylesheet" href="%s/style.css">
<style>
@import url("%s/import.css");
.box { background-image: url("%s/bg.png"); }
</style>
<video poster="%s/poster.png" width="80" height="40"></video>
<div class="box">Box</div>
<img src="http://169.254.169.254/latest/meta-data/" alt="m">
</div>`, srv.URL, srv.URL, srv.URL, srv.URL, srv.URL)

	res, err := c.Render(ctx, Request{HTML: frag})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 0 {
		t.Fatalf("external server was hit %d times, want 0", got)
	}

	shot := res.Shots[0]
	wantBlocked := []string{
		srv.URL + "/img.png",
		srv.URL + "/style.css",
		srv.URL + "/import.css",
		srv.URL + "/bg.png",
		srv.URL + "/poster.png",
		"http://169.254.169.254/latest/meta-data/",
	}
	blockedSet := map[string]string{}
	for _, b := range shot.Metrics.BlockedRequests {
		blockedSet[b.URL] = b.Reason
	}
	for _, u := range wantBlocked {
		reason, ok := blockedSet[u]
		if !ok {
			t.Errorf("URL not recorded as blocked: %s\nrecorded: %+v", u, shot.Metrics.BlockedRequests)
			continue
		}
		if reason == "" {
			t.Errorf("%s blocked without a reason", u)
		}
	}
}

// TestRenderConcurrency exercises the semaphore: more renders in parallel
// than RENDER_CONCURRENCY allows, all must succeed.
func TestRenderConcurrency(t *testing.T) {
	c := testChrome(t, Config{Concurrency: 2})
	if cap(c.sem) != 2 {
		t.Fatalf("semaphore cap = %d, want 2", cap(c.sem))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const n = 6
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := c.Render(ctx, Request{HTML: guideFragment, Variants: []Variant{{}}})
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
	}
}

// TestRenderCancelAndTimeout checks that Render honors cancellation and the
// per-render timeout.
func TestRenderCancelAndTimeout(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		c := testChrome(t, Config{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Render(ctx, Request{HTML: guideFragment})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("per-render timeout", func(t *testing.T) {
		c := testChrome(t, Config{Timeout: time.Nanosecond})
		_, err := c.Render(context.Background(), Request{HTML: guideFragment})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("deadline in the past", func(t *testing.T) {
		c := testChrome(t, Config{})
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := c.Render(ctx, Request{HTML: guideFragment})
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want a context error", err)
		}
	})
}

// TestRenderLazyWithoutBrowser proves construction never needs Chrome: the
// app can be built and validated on a machine without a browser.
func TestRenderLazyWithoutBrowser(t *testing.T) {
	c, err := New(Config{ChromePath: "/definitely/not/a/browser"})
	if err != nil {
		t.Fatalf("New must not need a browser: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.Render(context.Background(), Request{HTML: guideFragment})
	if !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("err = %v, want ErrNoBrowser", err)
	}
	// Invalid requests are still rejected before any browser lookup.
	_, err = c.Render(context.Background(), Request{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

// TestRenderRestartsDeadBrowser kills the browser process and checks the next
// render transparently restarts it (ping + lazy restart).
func TestRenderRestartsDeadBrowser(t *testing.T) {
	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if _, err := c.Render(ctx, Request{HTML: guideFragment}); err != nil {
		t.Fatalf("warm render: %v", err)
	}

	// Kill the browser process without marking the renderer dead — the ping
	// must notice.
	c.mu.Lock()
	allocCancel := c.allocCancel
	c.mu.Unlock()
	if allocCancel == nil {
		t.Fatal("allocator not started after a render")
	}
	allocCancel()

	if _, err := c.Render(ctx, Request{HTML: guideFragment}); err != nil {
		t.Fatalf("render after browser death: %v", err)
	}
}

// TestRenderFontsThroughProxy loads real Google Fonts through the egress
// proxy: CONNECT to an allowlisted host, DNS + TLS from this machine. Needs
// outbound network access, so it only runs with RENDER_TEST_NETWORK=1.
func TestRenderFontsThroughProxy(t *testing.T) {
	if os.Getenv("RENDER_TEST_NETWORK") != "1" {
		t.Skip("set RENDER_TEST_NETWORK=1 to run network tests")
	}
	c := testChrome(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	frag := `<div id="root" style="font-family:'Roboto',sans-serif;padding:8px">
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Roboto:wght@400&display=swap">
<span style="font-family:'Roboto',sans-serif">Roboto text</span>
</div>`
	res, err := c.Render(ctx, Request{HTML: frag})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	shot := res.Shots[0]
	if len(shot.Metrics.FailedRequests) != 0 {
		t.Errorf("failed requests (proxy/DNS/TLS): %+v", shot.Metrics.FailedRequests)
	}
	if len(shot.Metrics.BlockedRequests) != 0 {
		t.Errorf("blocked requests: %+v", shot.Metrics.BlockedRequests)
	}
	if len(shot.Metrics.TextRuns) == 0 {
		t.Error("no text runs")
	}
}
