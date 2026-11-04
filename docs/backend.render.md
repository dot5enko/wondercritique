# Backend — render (HTML fragment → screenshots + metrics)

State as of 2026-11-05 (`content_x` metric added). Package `internal/features/render` (`render.Module`) turns an untrusted
LLM-generated HTML component fragment into PNG screenshots plus layout/style metrics for the critic
LLMs (vision critics get the PNGs, text critics the metrics). Companion package
`internal/features/blobs` (`blobs.Module`) stores the PNGs. Chrome runs **on the production host**,
so security is the primary design constraint (see Security below).

## API

```go
type Renderer interface {
    Render(ctx context.Context, req Request) (*Result, error)
}
```

The module provides the interface and the concrete `*render.Chrome`. Construction is side-effect
free: the browser, the egress proxy and the Chrome path lookup all happen lazily on the first
`Render` (so `fx.ValidateApp` and app tests pass without Chrome installed), and everything is torn
down by an fx `OnStop` hook.

- `Request{HTML, Variants}` — `HTML` is the fragment (≤256 KiB, required); `Variants` empty means
  one `DefaultVariant`.
- `Variant` fields (JSON names are the API contract), zero values normalized:

| Field | Default | Limits |
|---|---|---|
| `name` | `"light"` / `"dark"` from `color_scheme` | `[a-zA-Z0-9_.-]{1,40}` |
| `width` | 480 (stage + viewport, CSS px) | 120–2400 |
| `stage_height` | 220 (stage min-height) | 40–2400 |
| `background` | `#ffffff` (light) / `#111214` (dark); `"transparent"` → alpha PNG | any CSS color without `;{}<>` quotes/backslashes |
| `color_scheme` | `light` | `light` \| `dark` (emulates `prefers-color-scheme` + `color-scheme`) |
| `device_scale_factor` | 2 | 1–3 |
| `max_height` | 1200 (capture cap) | `stage_height`–4800 |

- `Result{CanvasVersion, Shots[]}`; `Shot{Variant, PNG, Width, Height, Metrics}` — `Width`/`Height`
  are CSS px; the PNG is `× DeviceScaleFactor` pixels. `PNG` is `json:"-"`: callers persist it via
  the blob store and reference `wcblob://` URLs instead.
- Helpers: `render.LightDark()`, `render.DefaultVariant()`.
- Errors: `ErrInvalidRequest` (wraps all validation), `ErrNoBrowser` (no Chrome found and
  `RENDER_CHROME_PATH` unset/invalid), `ErrClosed`.
- A render job runs under `min(caller ctx, RENDER_TIMEOUT)`; cancellation/timeout tears the tab
  down and returns a wrapped `context.Canceled` / `context.DeadlineExceeded`.

## Canvas (renderer-owned, versioned)

`CanvasVersion = "wc-canvas/1"` — bump it whenever `canvas.html` or the CSP changes: screenshots of
the same fragment are only comparable within one canvas version. The canvas is a full document the
renderer owns: doctype, charset, CSP `<meta>`, `no-referrer`, `color-scheme`, a minimal reset
(border-box, zero margins), and the stage:

```html
<div id="wc-stage">          <!-- variant width, min-height = stage_height,
                                 flex row, centered; fragment injected verbatim -->
```

The variant background is applied to `html` (the stage itself is transparent), animations and
transitions are forced to their end state for deterministic capture. The fragment is injected
verbatim between the stage tags — the renderer does not validate its structure (that is
`internal/features/component`'s job).

The document is loaded as `data:text/html;base64,…`: no network request, no origin, no host to
resolve. Everything the fragment pulls in comes from the network policy below.

**Capture** = the stage area: `height = clamp(max(stage box height, content height), …, max_height)`
(rounded up), width = variant width; `Page.captureScreenshot` from the compositor surface, so PNG
pixels = CSS px × `device_scale_factor`.

## Metrics (controller-side; page JS disabled)

Measured with `DOMSnapshot.captureSnapshot` + a computed-style whitelist
(`color, background-color, font-size, font-weight, font-family`) — this works with script execution
disabled, and no `Runtime.evaluate` is ever issued.

| Metric | Meaning |
|---|---|
| `stage` | stage box in viewport coords (x=0) |
| `root` | component root element (first element child of the stage), **relative to the stage**; all zeros when that child has no box (a leading `<style>`/`<link>`) |
| `stage_height` | stage box height (≥ `stage_height`; grows with content) |
| `content_height` | bottom of everything inside the stage, from the stage top |
| `content_width` / `content_x` | width of the union of everything inside the stage and its left edge relative to the stage (negative = escapes left); used by the canvas PNG export crop |
| `overflow_x` / `overflow_y` | content escapes the stage box (±0.5 px tolerance) |
| `top_level_elements` | element children of the stage (a valid fragment has 1) |
| `blocked_requests` | `{url, reason}` — denied by CSP or the request filter (`content-security-policy`, `not-allowlisted`, `scheme-not-allowed`, `port-not-allowed`, `invalid-url`); capped at 50 |
| `failed_requests` | `{url, error}` — reached the network and failed (e.g. `net::ERR_PROXY_CONNECTION_FAILED`); capped at 50, never duplicates a blocked URL |
| `text_runs` | post-layout text runs (max 150, text truncated at 240 runes) |

Each text run: `text`, computed `color`, `background` (ancestor backgrounds folded over the canvas
background — white when the variant background is transparent — composited bottom-up, returned as
`rgb()`), computed `font_size` / `font_weight` / `font_family`, and `contrast`: the WCAG 2.x
contrast ratio of the (alpha-composited) text over that background; `0` means “not computable”
(unparseable color).

## Wait strategy (no page JS)

1. Navigate to the data: URL; `chromedp.Navigate` waits for the frame load event.
2. Wait until **no in-flight work**: zero open Fetch pauses (every paused request has been
   continued/failed by us) and zero in-flight Network requests — tracked per `requestWillBeSent` /
   `loadingFinished` / `loadingFailed` by request id, tolerant of a terminal event arriving before
   its `requestWillBeSent` (the Network domain does not order events across methods).
3. **Settle**: that state must hold for 120 ms (poll every 20 ms) — this is what lets Google Fonts
   (requested during layout, after the load event) land before the snapshot. Bounded by
   `RENDER_TIMEOUT`; a request that never completes fails the render rather than hanging it.
4. Resize the viewport to the capture height (30 ms settle) and screenshot.

## Security

Untrusted HTML in Chrome on the production host — five independent layers:

1. **No script, strict CSP.** `Emulation.setScriptExecutionDisabled` for every tab *and* a
   renderer-owned CSP meta:
   `default-src 'none'; script-src 'none'; style-src 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com data:; img-src data: <allowlist>; media-src data: <allowlist>; base-uri 'none'; form-action 'none'` (+ `frame-ancestors 'none'; sandbox` only in the header variant of `hostPolicy.csp`).
   Test: a fragment whose `<script>` and `<img onerror>` both try to inflate the root to 9999 px —
   metrics show the original height and the handler had no effect; the hostile image URL shows up in
   `blocked_requests`.
2. **CDP Fetch interception (per tab, `Fetch.enable` with `*`).** `data:`/`blob:`/`about:`/Chrome
   internals pass (they never leave the browser); everything else must be `https` on the default
   port to an allowlisted host (Google Fonts hosts + `RENDER_ALLOWED_HOSTS`), no userinfo —
   otherwise `Fetch.failRequest` and the URL+reason land in `Metrics.BlockedRequests`. Unit-tested
   as a pure decision table plus the record/terminal-event state machine.
   Note on layering: CSP is evaluated in the renderer *before* a request reaches the network
   service, so in practice most denials surface as `Network.loadingFailed(blockedReason=csp)` —
   those are recorded too (reason `content-security-policy`). Fetch is the second net for anything
   CSP lets through; both paths are deduplicated by URL.
3. **Egress proxy (defense in depth).** Chrome is launched with
   `--proxy-server=http://127.0.0.1:<random>` and `--proxy-bypass-list=<-loopback>` (so loopback is
   *not* bypassed — `127.0.0.1` goes through the proxy and is refused). The in-process Go proxy
   (`proxy.go`) accepts only `CONNECT <allowlisted-host>:443`; plain-HTTP proxy requests → 405,
   other hosts/ports → 403 (before any DNS). It resolves the hostname **once**, keeps only publicly
   routable addresses (`IsPublicAddr`: loopback, RFC1918/IPv6-ULA, link-local incl.
   `169.254.169.254`, CGNAT, multicast, unspecified, documentation, NAT64, 6to4, Teredo, … —
   IPv4-mapped IPv6 judged by the IPv4 part), then **dials the vetted IP literal** so there is no
   second lookup to race (no DNS rebinding); the dialer re-checks the address at connect time.
   Unit-tested with an injected resolver (`public.example → 127.0.0.1 / 169.254.169.254 / 10.x /
   ::ffff:… / fc00::…` all refused with the dialer never called; public IP dials
   `93.184.216.34:443` exactly, bytes flow both ways) and an integration test with an `httptest`
   server: `<img>`, `<link>`, CSS `url()`, `@import` and `<video poster>` referencing it produce
   **zero hits**, and `http://169.254.169.254/…` is blocked and recorded.
4. **Process isolation.** Every captured variant runs in a fresh browser context
   (`chromedp.WithNewBrowserContext`, incognito profile) that is disposed with the tab. One
   long-lived browser is started lazily, pinged (`Browser.getVersion`) before each render and
   restarted when dead; `RENDER_CONCURRENCY` (semaphore) caps parallel renders; the per-render
   timeout/cancel context is wired to tab teardown.
5. **Chrome flags.** chromedp's standard headless + privacy defaults (no first run, no background
   networking, no sync, no component updates, `disable-dev-shm-usage`, …), plus the proxy pair.
   `--no-sandbox` is **never** added by default — the flag is explicitly set to `false` (so chromedp
   cannot sneak it in when running as root) and only `RENDER_CHROME_NO_SANDBOX=true` adds it, with
   a logged warning.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `RENDER_CHROME_PATH` | (empty) | auto-detect: PATH (`google-chrome-stable`, `chromium`, …), well-known locations, Playwright caches (`ms-playwright/chromium-*`); resolved once and cached; an invalid explicit path is a hard error |
| `RENDER_CONCURRENCY` | 2 | parallel renders per process |
| `RENDER_TIMEOUT` | `20s` | hard deadline per `Render` call (queueing included) |
| `RENDER_ALLOWED_HOSTS` | (empty) | comma-separated extra hosts fragments may load from; plain DNS names only (no scheme/port/wildcard/IP), validated at startup |
| `RENDER_CHROME_NO_SANDBOX` | `false` | adds `--no-sandbox` (warned); never enable in production |

All documented in `apps/backend/.env.example`.

## Blobs (`internal/features/blobs`)

Content-addressed store for screenshots, SQLite via the app DB (`dbx.AsModel`, AutoMigrate):

```go
type Blob struct { SHA256 string /* pk, 64 hex */; MIME string; Size int64; Data []byte; CreatedAt time.Time }
```

- `Put(ctx, data, mime) (sha, error)` — sha256 hex; idempotent (`ON CONFLICT DO NOTHING`); mime
  defaults to `application/octet-stream`, invalid mime rejected.
- `Get(ctx, sha) (*Blob, error)` (`ErrNotFound`), `Exists`, `DataURL(ctx, sha)` →
  `data:<mime>;base64,…`.
- `URL(sha)` → `wcblob://<sha>`; `ParseURL(raw)` extracts and validates (`ErrInvalidSHA`).

## Tests

`cd apps/backend`:

- no Chrome needed: request/canvas/host-policy validation, color parsing + WCAG contrast, the
  Fetch decision table and record state machine, the proxy (fake resolver/dialer: public → vetted
  IP, loopback/link-local/RFC1918/CGNAT/mapped-IPv6/ULA refused, plain HTTP 405, tunnel bytes),
  blobs (in-memory SQLite).
- Chrome needed (skipped cleanly when `findChrome` finds nothing): guide light+dark render (PNG
  dims = CSS×DPR, metrics plausible, `prefers-color-scheme` changes the card background), over-tall
  content (400 px → content height > 220 and capture ≈400; 1500 px → capture capped at 1200),
  script/onerror no-effect, zero-hit external-request test, concurrency (6 renders on a semaphore
  of 2), cancel/timeout, lazy no-browser behaviour, browser-restart-after-death.
- network only with `RENDER_TEST_NETWORK=1`: Google Fonts loaded **through the egress proxy**
  (CONNECT + DNS + TLS) with no blocked/failed requests.
