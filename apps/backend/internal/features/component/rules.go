package component

import "slices"

// Severity of a violation. Only error-severity violations make a fragment invalid.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Phases a rule can run in.
const (
	PhaseStatic = "static" // Validate: from the HTML/CSS source text
	PhaseLayout = "layout" // CheckLayout: from renderer measurements
)

// Stable rule codes. They are part of the contract with the generator/critic prompts and the UI.
const (
	CodeTooLarge         = "too-large"
	CodeDocumentWrapper  = "no-document-wrapper"
	CodeScript           = "no-script"
	CodeIframe           = "no-iframe"
	CodeStyleBlock       = "style-block"
	CodeFontLinks        = "font-links"
	CodeSingleRoot       = "single-root"
	CodeRootSelectors    = "no-root-selectors"
	CodeUniversalReset   = "no-universal-reset"
	CodeRootTokens       = "root-tokens-only"
	CodeScopedCSS        = "scoped-css"
	CodeCSSImport        = "css-import"
	CodeExternalResource = "external-resource"
	CodeStageWrapper     = "no-stage-wrapper"
	CodeStageHeight      = "stage-height"
	CodeOpaqueStage      = "opaque-stage"
	CodeOverflowX        = "overflow-x"
	// CodeInternal is reported (as an error) only if the validator itself failed on the input; it fails closed.
	CodeInternal = "internal-error"
)

// Rule describes one check, for catalogs, UIs and prompts.
type Rule struct {
	Code        string   `json:"code"`
	Severity    Severity `json:"severity"`
	Phase       string   `json:"phase"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
}

var ruleCatalog = []Rule{
	{CodeTooLarge, SeverityError, PhaseStatic, "Fragment too large",
		"The fragment is larger than 200 KB. Nothing else is checked."},
	{CodeDocumentWrapper, SeverityError, PhaseStatic, "No document wrapper",
		"Send only the component fragment: no <!doctype>, <html>, <head>, <body>, <meta>, <title> or <base>. The canvas builds the document. (<title> inside inline SVG is fine.)"},
	{CodeScript, SeverityError, PhaseStatic, "No JavaScript",
		"No <script> (including inside SVG), no <noscript>, no on* event-handler attributes, no javascript:/vbscript: URLs, and no SVG animations that set on* attributes. The preview is sandboxed and runs no JS."},
	{CodeIframe, SeverityError, PhaseStatic, "No nested documents",
		"No <iframe>, <frame>, <frameset>, <object>, <embed>, <portal>, <fencedframe> or <applet>. Nested documents break auto-height; use inline SVG, <img> or <video>."},
	{CodeStyleBlock, SeverityError, PhaseStatic, "At most one closed <style>",
		"At most ONE <style> block (counting <style> inside SVG), and it must be closed. Components that only use inline style=\"\" attributes are fine."},
	{CodeFontLinks, SeverityError, PhaseStatic, "Google Fonts links only",
		"0–4 <link> tags, and only Google Fonts: rel=\"stylesheet\" to https://fonts.googleapis.com, or rel=\"preconnect\" to https://fonts.googleapis.com or https://fonts.gstatic.com."},
	{CodeSingleRoot, SeverityError, PhaseStatic, "Exactly one root element",
		"Besides the optional <link>s and one <style> (and whitespace/comments), the fragment has exactly one top-level element and no stray top-level text."},
	{CodeRootSelectors, SeverityError, PhaseStatic, "No html/body selectors",
		"No selector may target html or body as a type selector, anywhere in the selector (including :is()/:where()/:has())."},
	{CodeUniversalReset, SeverityError, PhaseStatic, "No global * reset",
		"No unscoped universal selector such as *, *::before, ::selection or * + *. A universal selector scoped under a component class (.c1-card *) is fine."},
	{CodeRootTokens, SeverityError, PhaseStatic, ":root holds only tokens",
		"Rules whose subject is :root may only declare --* custom properties (design tokens)."},
	{CodeScopedCSS, SeverityError, PhaseStatic, "Component-scoped CSS",
		"Every selector must select the component root or its descendants by a class or id from the markup (including inside @media/@supports/@container/@layer/@scope and nested rules). Error when the selector has no class/id anchor (h3, div > p, [data-x]) or escapes the component (.root + p, div:has(.x)); warning when it uses a class/id that does not appear in the markup. :root token rules, @keyframes and @font-face are exempt."},
	{CodeCSSImport, SeverityError, PhaseStatic, "No CSS @import",
		"@import is only allowed for https://fonts.googleapis.com font CSS."},
	{CodeExternalResource, SeverityWarning, PhaseStatic, "External resources won't load",
		"CSS url(...) and <img>/<video>/<audio>/<source>/<track>/SVG <image>/<use> sources should be data: URIs (or Google Fonts). The renderer blocks other network hosts and relative URLs have nothing to resolve against, so those resources won't show."},
	{CodeStageWrapper, SeverityWarning, PhaseStatic, "No fullscreen stage wrapper (static)",
		"Heuristic: the root element (inline style or CSS rules on its classes/id) uses position: fixed or viewport-sized width/height (100vw, 100vh, 100dvh, min-height: 100vh…). The canvas owns the stage and centering."},
	{CodeStageHeight, SeverityError, PhaseLayout, "Fits the stage height",
		"The rendered component must fit inside the stage height (220px by default)."},
	{CodeStageWrapper, SeverityError, PhaseLayout, "No fullscreen stage wrapper (layout)",
		"The rendered root element covers the whole viewport, or uses position: fixed."},
	{CodeOpaqueStage, SeverityWarning, PhaseLayout, "Transparent stage",
		"The root element spans the full stage and paints an opaque background. Keep the background transparent unless the component itself paints it."},
	{CodeOverflowX, SeverityError, PhaseLayout, "No horizontal overflow",
		"The rendered content must not be wider than the viewport."},
	{CodeInternal, SeverityError, PhaseStatic, "Validator failure",
		"The validator could not analyse the input. This fails closed; simplify the markup."},
}

// Rules returns the rule catalog in a stable order. no-stage-wrapper appears twice: once per phase,
// with different severities.
func Rules() []Rule { return slices.Clone(ruleCatalog) }
