# Component fragment validator (`internal/features/component`)

State as of 2026-11-01.

## Purpose

Wondercritique iteratively improves web component designs. A generator LLM writes an HTML
**fragment**: optional Google-Fonts `<link>`s, optional one `<style>`, exactly one component
element — no document wrappers, no scripts. The package `apps/backend/internal/features/component`
is a pure library (no fx module, no HTTP) that validates such fragments:

1. **self-check tool** — the generator calls `Validate` while iterating;
2. **hard gate** — `Report.OK()` decides whether a fragment may be rendered;
3. **report for critics** — messages are written to be precise and actionable for an LLM
   (`Format` renders them as one compact line each).

`CheckLayout` is the post-render phase: the renderer (not yet in the backend) measures the
mounted component and feeds `LayoutFacts` back for rules that only exist after painting.

Dependencies: `golang.org/x/net/html` (tokenizer) and `github.com/tdewolff/parse/v2` (CSS lexer).

## API

```go
type Severity string                    // SeverityError | SeverityWarning

type Violation struct {
    Code     string   // stable rule code (below)
    Severity Severity
    Message  string   // actionable, LLM-facing
    Line     int      // 1-based; 0 = unknown (too-large, layout, internal-error)
    Snippet  string   // offending source line (truncated) or layout fact summary
}

type Fragment struct {
    Bytes       int      // len(input)
    RootTag     string   // tag of the single root element; "" unless exactly one root
    Links       []string // href of each <link>, in document order
    StyleBlocks int      // number of <style> elements anywhere (incl. inside SVG)
    Classes     []string // sorted unique class tokens in the markup
    IDs         []string // sorted unique ids in the markup
}

type Report struct {
    Violations []Violation
    Fragment   Fragment
}

func Validate(src string) *Report // never panics; fails closed with internal-error
func (r *Report) OK() bool         // true iff no error-severity violation (warnings pass)
func Format(vs []Violation) string // "error no-script (line 12): …" one per line

type Rule struct { Code, Severity, Phase, Title, Description string }
func Rules() []Rule // stable catalog; Phase is "static" or "layout"

type LayoutFacts struct { … }            // see below
func CheckLayout(f LayoutFacts) []Violation // Line 0; Snippet = fact summary
```

`Validate` never panics: any internal failure is recovered and reported as a single
`internal-error` violation (fail closed). Findings are grouped in this order: markup findings in
token order → structural findings (duplicate/unclosed `<style>`, link count) → single-root
findings → CSS findings per `<style>` block in document order → root inline `style=""` findings.
Line numbers come from tokenizer raw-byte offsets (exact); CSS findings are offset by the
`<style>` content start.

## Rules

All are `static` phase (from `Validate`) unless marked *layout* (`CheckLayout`). Severity as
noted; everything not marked warning is an error.

| Code | Sev | Phase | Checks |
|---|---|---|---|
| `too-large` | error | static | input > 200 KiB (204 800 bytes). Nothing else is checked. |
| `no-document-wrapper` | error | static | `<!doctype>`, `<html>`, `<head>`, `<body>`, `<meta>`, `<title>`, `<base>` anywhere, detected at tokenizer level (the fragment parser would drop them silently). `<title>` inside inline SVG is exempt. |
| `no-script` | error | static | any `<script>` (incl. inside inline SVG, `<SCRIPT>`), `<noscript>`, any `on*` attribute, `javascript:`/`vbscript:` URLs in `href/src/action/formaction/xlink:href/poster/data/…` (entities decoded, tab/newline stripped, whitespace trimmed, case-insensitive), SVG `<set>/<animate… attributeName="on*">`. |
| `no-iframe` | error | static | `iframe`, `frame`, `frameset`, `object`, `embed`, `portal`, `fencedframe`, `applet`. |
| `style-block` | error | static | more than one `<style>` anywhere (incl. inside SVG), or a `<style>` never closed. |
| `font-links` | error | static | more than 4 `<link>`s; any link that is not Google Fonts: `rel="stylesheet"` → `https://fonts.googleapis.com`, `rel="preconnect"` → `https://fonts.googleapis.com` or `https://fonts.gstatic.com`, https only, exact host. |
| `single-root` | error | static | exactly one top-level element besides links/one style/whitespace/comments; stray top-level text counts too; 0 roots and >1 roots both fail. |
| `no-root-selectors` | error | static | any `html`/`body` type selector anywhere in a selector (incl. inside `:is()/:where()/:has()/…`). |
| `no-universal-reset` | error | static | subject is a bare global `*` (e.g. `*`, `*::before`, `* + *`, `::selection`) and the selector has no class, id, type or `&` anywhere. `.c1-card *` is fine; `div *` falls through to `scoped-css`. |
| `root-tokens-only` | error | static | a rule whose subject is `:root` (e.g. `:root`, `:root::before`) may only declare `--*` custom properties. |
| `scoped-css` | error / warning | static | every selector in every style rule (incl. inside `@media/@supports/@container/@layer/@scope` and nested rules) must be anchored on a class or id present in the markup. **error** when the selector has no class/id at all (e.g. `h3 {}`); **warning** when its class/id isn't in the markup. `:root` token rules, `@keyframes` and `@font-face` are exempt; `&` counts as an anchor. |
| `css-import` | error | static | `@import` not pointing at `https://fonts.googleapis.com`. |
| `external-resource` | warning | static | CSS `url(...)` or `<img src>` / `<video src|poster>` / `<source|audio|track|input src>` / SVG `<image|use href|xlink:href>` other than `data:`, `#fragment` or Google Fonts hosts (relative URLs have nothing to resolve against). |
| `no-stage-wrapper` | warning | static | heuristic on the root element (its inline `style=""` or CSS rules whose subject contains a root class/id): `position: fixed`, or `width/height/min-width/min-height` sized `100vw`/`100vh`/`100dvh`. |
| `stage-height` | error | *layout* | content taller than the stage (default 220 px). |
| `no-stage-wrapper` | error | *layout* | root box covers the whole stage (origin ≤ 0,0 and size ≥ stage) or `position: fixed`. |
| `opaque-stage` | warning | *layout* | root covers the stage and paints an opaque background. |
| `overflow-x` | error | *layout* | content wider than the stage viewport. |
| `internal-error` | error | static | the validator failed on the input; fails closed. |

`no-stage-wrapper` appears twice in `Rules()` — once per phase, with different severities.
Per selector part at most one selector rule fires, in precedence order:
`:root` → `no-root-selectors` → `no-universal-reset` → `scoped-css`.

## LayoutFacts

Supplied by the renderer after the fragment is mounted in the stage (px; unknown = 0):

| Field | Meaning / effect |
|---|---|
| `StageWidth` | stage viewport width. 0 → `overflow-x` and the "covers the stage" test are skipped. |
| `StageHeight` | stage viewport height. ≤ 0 → `DefaultStageHeight` (220). |
| `ContentWidth` | content `scrollWidth`. 0 → falls back to `RootWidth`. |
| `ContentHeight` | content `scrollHeight`. ≤ 0 → falls back to `RootHeight`. |
| `RootLeft`, `RootTop` | root border-box origin relative to the stage. |
| `RootWidth`, `RootHeight` | root border-box size. |
| `RootPosition` | computed position of the root (`static/relative/absolute/fixed/sticky`). |
| `RootBackground` | computed background-color of the root (`#fff`, `rgba(…, 0)`, `""` = transparent/unknown). |

Comparisons use a 0.5 px epsilon. `CheckLayout` returns findings with `Line 0` and a fact
summary (`root 800x600 at (0,0); stage 800x220`) in `Snippet`.

## Heuristic limits

- **Unknown at-rules** (`@tailwind`, custom at-rules, …) have their blocks skipped entirely;
  known rule-list at-rules are `media supports container scope layer document starting-style`,
  declaration-list: `font-face page counter-style property view-transition`, `keyframes` (after
  vendor-prefix stripping) exempts its selectors.
- **CSS identifiers are compared as written**: escapes are not decoded (`.c1\20 card` will not
  match `class="c1 card"`), and case is significant for classes/ids (as in HTML) but not for
  type selectors.
- **Attribute selectors are not anchors**: `[data-x]` still triggers `scoped-css` (error);
  `[class~="c1-card"]` is not recognised as one.
- **Selector-taking pseudos** are recursed into: `is where has not matches any host
  host-context slotted current` (+ vendor `any`). `:nth-child(2 of .x)` is not analysed.
- **Recursion is capped** (`maxCSSDepth = 64`) for nested rules/at-rules and pseudo nesting;
  deeper content is skipped for analysis but still parsed for positions.
- **`no-stage-wrapper` (static)** only inspects rules whose subject compound contains a root
  class/id (`.c1-card`, `#id`, `.c1-card:hover`), and only `position` + `width/height/min-*`
  properties; `max-height: 100vh`, `100svh/lvh` and `position: sticky` are not flagged.
  It is skipped entirely when the fragment does not have exactly one root.
- **`external-resource`** checks `url(...)` (not `image-set()` string arguments) and a fixed
  attribute list; `<a href>` is intentionally not checked; `iframe/object/embed` sources are
  covered by `no-iframe` instead.
- **`srcset`** is checked as a whole string (only a leading `javascript:` is detected).
- **`too-large`** stops the analysis at 200 KiB; line numbers are byte-based (`\n` counted).
- **Layout "covers"** requires a known `StageWidth`; `opaqueBackground` treats any unknown but
  non-empty color as opaque (warning severity only).
