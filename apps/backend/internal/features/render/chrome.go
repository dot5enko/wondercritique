package render

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Config configures the Chrome-backed renderer.
type Config struct {
	// ChromePath is the browser executable. Empty means auto-detect (see findChrome);
	// the result is cached for the lifetime of the Chrome.
	ChromePath string
	// Concurrency caps parallel renders (default 2).
	Concurrency int
	// Timeout bounds one Render call, including queueing (default 20s).
	Timeout time.Duration
	// AllowedHosts are extra hosts, besides Google Fonts, that the fragment may
	// load from (validated by newHostPolicy).
	AllowedHosts []string
	// NoSandbox adds --no-sandbox to Chrome. Off by default; logged as a warning.
	NoSandbox bool
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

const (
	defaultConcurrency   = 2
	defaultRenderTimeout = 20 * time.Second
	browserPingTimeout   = 2 * time.Second
	settleDuration       = 120 * time.Millisecond
	quietPollInterval    = 20 * time.Millisecond
	viewportSettle       = 30 * time.Millisecond
)

// Chrome is the CDP-backed Renderer: one long-lived headless Chrome, started
// lazily on the first render and restarted when it is dead, with a fresh
// browser context (incognito profile) per captured variant. It is safe for
// concurrent use up to Config.Concurrency.
type Chrome struct {
	cfg    Config
	policy *hostPolicy
	log    *slog.Logger
	sem    chan struct{}

	mu            sync.Mutex
	closed        bool
	dead          bool
	chromePath    string
	proxy         *Proxy
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

var _ Renderer = (*Chrome)(nil)

// New builds a renderer. It never starts a browser: the browser, the egress
// proxy and the Chrome executable lookup all happen on the first Render, so
// constructing the app does not require Chrome to be installed.
func New(cfg Config) (*Chrome, error) {
	policy, err := newHostPolicy(cfg.AllowedHosts)
	if err != nil {
		return nil, err
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultRenderTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Chrome{
		cfg:    cfg,
		policy: policy,
		log:    log.With("component", "render"),
		sem:    make(chan struct{}, cfg.Concurrency),
	}, nil
}

// Render captures req.HTML in every requested variant. See docs/backend.render.md.
func (c *Chrome) Render(ctx context.Context, req Request) (*Result, error) {
	variants, err := req.normalize()
	if err != nil {
		return nil, err
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("render: %w", ctx.Err())
	}

	rctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	if err := rctx.Err(); err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}

	browserCtx, err := c.ensureBrowser(rctx)
	if err != nil {
		return nil, err
	}

	out := &Result{CanvasVersion: CanvasVersion, Shots: make([]Shot, 0, len(variants))}
	for i, v := range variants {
		shot, err := c.renderVariant(rctx, browserCtx, i, v, req.HTML)
		if err != nil {
			if rerr := rctx.Err(); rerr != nil {
				return nil, fmt.Errorf("render: %w", rerr)
			}
			if isBrowserExit(err) {
				c.markDead()
			}
			return nil, err
		}
		out.Shots = append(out.Shots, *shot)
	}
	return out, nil
}

// Close stops the browser, the allocator and the egress proxy. Idempotent.
func (c *Chrome) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.stopBrowserLocked()
	if c.proxy != nil {
		_ = c.proxy.Close()
		c.proxy = nil
	}
	return nil
}

// ensureBrowser returns a live browser context, starting (or restarting) the
// browser if needed. It pings the browser first, so a crashed Chrome is
// replaced before the render starts.
func (c *Chrome) ensureBrowser(ctx context.Context) (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if c.browserCtx != nil && !c.dead {
		// The ping must run on the chromedp context itself (deriving from the
		// caller's context would drop chromedp's context values), bounded by a
		// timeout so a hung browser does not block the queue.
		pctx, pcancel := context.WithTimeout(c.browserCtx, browserPingTimeout)
		defer pcancel()
		_, pingErr := chromedp.CallBrowser(pctx, browser.GetVersion, cdp.Empty{})
		if pingErr == nil {
			return c.browserCtx, nil
		}
		c.log.Warn("browser not responding, restarting", "err", pingErr)
		c.stopBrowserLocked()
	}
	if err := c.startBrowserLocked(ctx); err != nil {
		return nil, err
	}
	return c.browserCtx, nil
}

// startBrowserLocked starts the egress proxy (once) and the browser process.
func (c *Chrome) startBrowserLocked(ctx context.Context) error {
	path, err := c.findChromeLocked()
	if err != nil {
		return err
	}
	if c.proxy == nil {
		proxy, err := StartProxy(ProxyConfig{
			Allow:  c.policy.allowsHost,
			Logger: c.log,
		})
		if err != nil {
			return err
		}
		c.proxy = proxy
		c.log.Info("egress proxy started", "addr", proxy.Addr())
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), c.execOptions(path, c.proxy.URL())...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// First call allocates the browser process. chromedp must not run this on a
	// context with a timeout of its own (that would tie the browser lifetime to
	// it), so bound it from outside and kill the allocator if ctx gives up.
	done := make(chan error, 1)
	go func() {
		_, err := chromedp.CallBrowser(browserCtx, browser.GetVersion, cdp.Empty{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			allocCancel()
			browserCancel()
			return fmt.Errorf("render: start chrome: %w", err)
		}
	case <-ctx.Done():
		allocCancel()
		browserCancel()
		return fmt.Errorf("render: start chrome: %w", ctx.Err())
	}

	c.allocCancel = allocCancel
	c.browserCtx = browserCtx
	c.browserCancel = browserCancel
	c.dead = false
	if c.cfg.NoSandbox {
		c.log.Warn("Chrome sandbox disabled (RENDER_CHROME_NO_SANDBOX=true)")
	}
	c.log.Info("chrome started", "path", path)
	return nil
}

func (c *Chrome) stopBrowserLocked() {
	if c.browserCancel != nil {
		c.browserCancel()
		c.browserCancel = nil
	}
	if c.allocCancel != nil {
		c.allocCancel()
		c.allocCancel = nil
	}
	c.browserCtx = nil
	c.dead = true
}

func (c *Chrome) markDead() {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()
}

// execOptions builds the Chrome flags: chromedp's standard headless + privacy
// flags, headless, our proxy pair and — only when explicitly requested —
// --no-sandbox.
func (c *Chrome) execOptions(path, proxyURL string) []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path))
	for k, v := range c.chromeFlags(proxyURL) {
		opts = append(opts, chromedp.Flag(k, v))
	}
	return opts
}

// chromeFlags are the renderer's own flags on top of chromedp's defaults
// (headless, no-first-run, disable-background-networking, disable-sync,
// safebrowsing-disable-auto-update, disable-dev-shm-usage, …).
func (c *Chrome) chromeFlags(proxyURL string) map[string]any {
	flags := map[string]any{
		// Force the sandbox on unless the operator opted out; this also stops
		// chromedp from silently adding --no-sandbox when running as root.
		"no-sandbox": false,
		// Defense in depth: every request goes through our egress proxy, and
		// <-loopback disables Chrome's "bypass proxy for loopback" default so
		// 127.0.0.1 is proxied too (and then refused by the proxy).
		"proxy-server":      proxyURL,
		"proxy-bypass-list": "<-loopback>",
		// One disable-flags value: chromedp's defaults (Translate,
		// BlinkGenPropertyTrees) plus everything else we do not want phoning
		// home from a rendering host.
		"disable-features": "Translate,BlinkGenPropertyTrees,OptimizationHints,MediaRouter," +
			"InterestFeedContentSuggestions,AutofillServerCommunication",
		"disable-component-update":   true,
		"disable-domain-reliability": true,
		"disable-field-trial-config": true,
		"no-ping":                    true,
	}
	if c.cfg.NoSandbox {
		flags["no-sandbox"] = true
	}
	return flags
}

// renderVariant captures one variant in a fresh browser context.
func (c *Chrome) renderVariant(ctx context.Context, browserCtx context.Context, idx int, v Variant, html string) (*Shot, error) {
	tabCtx, tabCancel := chromedp.NewContext(browserCtx, chromedp.WithNewBrowserContext())
	defer tabCancel()
	// The per-render timeout/cancel must tear the tab down; tabCtx itself is
	// derived from the long-lived browser context.
	stop := context.AfterFunc(ctx, func() { tabCancel() })
	defer stop()

	doc, err := buildCanvas(c.policy, v, html, true)
	if err != nil {
		return nil, err
	}
	docURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(doc))

	filter := newRequestFilter(c.policy)
	if err := chromedp.Do(tabCtx,
		cdpVoid(emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true}),
		cdpVoid(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: int64(v.Width), Height: int64(v.StageHeight), DeviceScaleFactor: v.DeviceScaleFactor,
		}),
		cdpVoid(emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
			Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: v.ColorScheme}},
		}),
		cdpVoid(page.Enable, page.EnableParams{}),
		cdpVoid(network.Enable, network.EnableParams{}),
		cdpVoid(fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*"}}}),
	); err != nil {
		return nil, fmt.Errorf("render: variant %s setup: %w", v.Name, err)
	}

	filter.start(tabCtx)

	// Wait strategy with page JS disabled: the load event (chromedp.Navigate
	// waits for it), then no in-flight allowed request, then a short settle.
	if err := chromedp.Do(tabCtx, chromedp.Navigate(docURL)); err != nil {
		return nil, fmt.Errorf("render: variant %s navigate: %w", v.Name, err)
	}
	if err := waitQuiet(ctx, filter); err != nil {
		return nil, fmt.Errorf("render: variant %s wait: %w", v.Name, err)
	}

	snap, err := chromedp.Run(tabCtx, cdpRun(domsnapshot.CaptureSnapshot, domsnapshot.CaptureSnapshotParams{
		ComputedStyles: measuredStyles,
	}))
	if err != nil {
		return nil, fmt.Errorf("render: variant %s snapshot: %w", v.Name, err)
	}
	metrics, captureHeight, err := computeMetrics(&snap, v, filter)
	if err != nil {
		return nil, fmt.Errorf("render: variant %s metrics: %w", v.Name, err)
	}

	// Grow the viewport to the capture height so the surface covers the whole
	// clip, let it settle, then screenshot the stage area.
	if err := chromedp.Do(tabCtx,
		cdpVoid(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: int64(v.Width), Height: int64(captureHeight), DeviceScaleFactor: v.DeviceScaleFactor,
		}),
	); err != nil {
		return nil, fmt.Errorf("render: variant %s resize: %w", v.Name, err)
	}
	select {
	case <-time.After(viewportSettle):
	case <-ctx.Done():
		return nil, fmt.Errorf("render: %w", ctx.Err())
	}

	png, err := chromedp.Run(tabCtx, cdpRun(page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: page.CaptureScreenshotFormatPng,
		Clip: &page.Viewport{
			X: 0, Y: 0, Width: float64(v.Width), Height: float64(captureHeight), Scale: 1,
		},
		FromSurface: ptr(true),
	}))
	if err != nil {
		return nil, fmt.Errorf("render: variant %s screenshot: %w", v.Name, err)
	}
	if len(png.Data) == 0 {
		return nil, fmt.Errorf("render: variant %s: empty screenshot", v.Name)
	}

	return &Shot{
		Variant: v,
		PNG:     png.Data,
		Width:   v.Width,
		Height:  captureHeight,
		Metrics: *metrics,
	}, nil
}

// waitQuiet blocks until the load has settled: no open Fetch interception and
// no in-flight network request, sustained for settleDuration. ctx (the
// per-render timeout) bounds the wait.
func waitQuiet(ctx context.Context, f *requestFilter) error {
	deadline := time.NewTimer(0)
	defer deadline.Stop()
	quietSince := time.Time{}
	for {
		paused, pending := f.inFlight()
		now := time.Now()
		if paused == 0 && pending == 0 {
			if quietSince.IsZero() {
				quietSince = now
			}
			if now.Sub(quietSince) >= settleDuration {
				return nil
			}
		} else {
			quietSince = time.Time{}
		}
		deadline.Reset(quietPollInterval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
		}
	}
}

// findChromeLocked resolves and caches the Chrome executable.
func (c *Chrome) findChromeLocked() (string, error) {
	if c.chromePath != "" {
		return c.chromePath, nil
	}
	path, err := findChrome(c.cfg.ChromePath)
	if err != nil {
		return "", err
	}
	c.chromePath = path
	return path, nil
}

// findChrome returns the first usable Chrome/Chromium executable: the explicit
// path if given (a bad explicit path is an error, not a fallback), then
// PATH lookups, then well-known install locations, then Playwright caches.
func findChrome(explicit string) (string, error) {
	if explicit != "" {
		if st, err := os.Stat(explicit); err == nil && !st.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("%w: RENDER_CHROME_PATH=%q is not an executable file", ErrNoBrowser, explicit)
	}
	for _, name := range []string{
		"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
		"chrome", "chrome-headless-shell", "headless_shell", "microsoft-edge",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "linux":
		candidates = []string{
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/opt/google/chrome/chrome",
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if home != "" {
		patterns := []string{
			filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac/Chromium.app/Contents/MacOS/Chromium"),
			filepath.Join(home, "Library/Caches/ms-playwright/chromium_headless_shell-*/chrome-mac/headless_shell"),
			filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux/chrome"),
			filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-linux/headless_shell"),
		}
		for _, pat := range patterns {
			if hits, err := filepath.Glob(pat); err == nil {
				for _, p := range hits {
					if st, err := os.Stat(p); err == nil && !st.IsDir() {
						return p, nil
					}
				}
			}
		}
	}
	return "", ErrNoBrowser
}

// cdpVoid adapts a CDP command without a result to an action.
func cdpVoid[P any](cmd cdp.Command[P, cdp.Empty], params P) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, t, cmd, params)
		return chromedp.Void{}, err
	}
}

// cdpRun adapts a CDP command to an action returning its result.
func cdpRun[P, R any](cmd cdp.Command[P, R], params P) chromedp.Action[R] {
	return func(ctx context.Context, t *chromedp.Target) (R, error) {
		return cdp.Call(ctx, t, cmd, params)
	}
}

func ptr[T any](v T) *T { return &v }

// isBrowserExit reports whether err means the browser process is gone.
func isBrowserExit(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "chromium has unexpectedly exited") ||
		strings.Contains(msg, "chrome exited unexpectedly") ||
		strings.Contains(msg, "websocket: close 1006")
}
