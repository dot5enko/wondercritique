package component

import (
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
)

// maxCSSDepth bounds recursive descent (nested rules, at-rules, selector
// pseudo-functions) so pathological input cannot exhaust the stack.
const maxCSSDepth = 64

// At-rules whose block is a list of rules.
var ruleListAtRules = map[string]bool{
	"media": true, "supports": true, "container": true, "scope": true,
	"layer": true, "document": true, "starting-style": true,
}

// At-rules whose block is a list of declarations.
var declListAtRules = map[string]bool{
	"font-face": true, "page": true, "counter-style": true,
	"property": true, "view-transition": true,
}

// Pseudos whose arguments are selectors and must be analysed too.
var selectorPseudos = map[string]bool{
	"is": true, "where": true, "has": true, "not": true, "matches": true,
	"any": true, "-webkit-any": true, "-moz-any": true,
	"host": true, "host-context": true, "slotted": true, "current": true,
}

type cssTok struct {
	tt    css.TokenType
	data  string
	start int // offset within the analysed text (style block or style attr)
}

type cssChecker struct {
	lines       *lineIndex
	origin      int // absolute offset of toks[0] within the fragment
	toks        []cssTok
	out         *[]Violation
	markupClass map[string]bool
	markupID    map[string]bool
	rootClass   map[string]bool
	rootID      map[string]bool
}

type walkCtx struct {
	keyframes bool
	depth     int
}

type declOpts struct {
	rootRule    bool // :root rule: only --* declarations allowed
	stageTarget bool // declarations apply to the root element (stage-wrapper checks)
	depth       int
}

type selSummary struct {
	rootRule bool // some part's subject is :root
	hitsRoot bool // some part's subject is the root element
}

// checkRootInline analyses the root element's inline style="" attribute.
func checkRootInline(lines *lineIndex, s *scanResult) []Violation {
	if s.rootTag == "" {
		return nil
	}
	style := attrVal(s.rootAttrs, "style")
	if strings.TrimSpace(style) == "" {
		return nil
	}
	var vs []Violation
	c := newCSSChecker(lines, s, &vs, s.rootOff)
	c.toks = lexCSS(style)
	c.decls(0, len(c.toks), declOpts{stageTarget: true})
	return vs
}

// checkStyleBlock analyses one <style> block.
func checkStyleBlock(lines *lineIndex, s *scanResult, b styleBlock) []Violation {
	if b.content == "" {
		return nil
	}
	var vs []Violation
	c := newCSSChecker(lines, s, &vs, b.cStart)
	c.toks = lexCSS(b.content)
	c.walk(0, len(c.toks), walkCtx{})
	return vs
}

func newCSSChecker(lines *lineIndex, s *scanResult, out *[]Violation, origin int) *cssChecker {
	return &cssChecker{
		lines:       lines,
		origin:      origin,
		out:         out,
		markupClass: s.classSet,
		markupID:    s.idSet,
		rootClass:   stringSet(strings.Fields(attrVal(s.rootAttrs, "class"))),
		rootID:      stringSet(singleList(attrVal(s.rootAttrs, "id"))),
	}
}

func singleList(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

// lexCSS tokenizes CSS source. ErrorToken means EOF for this lexer.
func lexCSS(src string) []cssTok {
	if src == "" {
		return nil
	}
	in := parse.NewInputString(src)
	l := css.NewLexer(in)
	toks := make([]cssTok, 0, len(src)/8+4)
	prevEnd := 0
	for {
		tt, data := l.Next()
		end := in.Offset()
		if tt == css.ErrorToken {
			break
		}
		if end <= prevEnd {
			break // no progress: defensive, must not loop forever
		}
		toks = append(toks, cssTok{tt: tt, data: string(data), start: end - len(data)})
		prevEnd = end
	}
	return toks
}

func (c *cssChecker) line(rel int) int       { return c.lines.lineAt(c.origin + rel) }
func (c *cssChecker) snippet(rel int) string { return c.lines.snippetAt(c.origin + rel) }

func (c *cssChecker) add(code string, sev Severity, rel int, msg string) {
	*c.out = append(*c.out, newViolation(code, sev, c.line(rel), c.snippet(rel), msg))
}

// walk parses a rule list (stylesheet or at-rule body).
func (c *cssChecker) walk(from, to int, ctx walkCtx) {
	if to > len(c.toks) {
		to = len(c.toks)
	}
	i := from
	for i < to {
		var next int
		switch c.toks[i].tt {
		case css.WhitespaceToken, css.CommentToken, css.SemicolonToken, css.CommaToken,
			css.RightBraceToken, css.RightParenthesisToken, css.RightBracketToken,
			css.CDOToken, css.CDCToken:
			next = i + 1
		case css.AtKeywordToken:
			next = c.atRule(i, to, ctx)
		default:
			next = c.qualifiedRule(i, to, ctx)
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
}

// atRule handles "@name prelude { body }" and "@name prelude;". Returns the
// index just past the at-rule.
func (c *cssChecker) atRule(i, to int, ctx walkCtx) int {
	name := atRuleName(c.toks[i].data)

	// prelude: up to '{' or ';' at paren level 0
	j := i + 1
	level := 0
	blockAt := -1
	for j < to {
		tt := c.toks[j].tt
		if tt == css.LeftBraceToken && level == 0 {
			blockAt = j
			break
		}
		if tt == css.SemicolonToken {
			break
		}
		level += depthDelta(tt)
		if level < 0 {
			level = 0
		}
		j++
	}

	if blockAt < 0 {
		if name == "import" {
			c.checkImport(i+1, j)
		}
		if j < to && c.toks[j].tt == css.SemicolonToken {
			return j + 1
		}
		if j > i {
			return j
		}
		return i + 1
	}

	closeIdx := c.matchingBrace(blockAt, to)
	if name == "import" {
		c.checkImport(i+1, blockAt)
	}
	bodyFrom, bodyTo := blockAt+1, closeIdx
	if ctx.depth < maxCSSDepth {
		switch {
		case name == "keyframes":
			c.walk(bodyFrom, bodyTo, walkCtx{keyframes: true, depth: ctx.depth + 1})
		case ruleListAtRules[name]:
			c.walk(bodyFrom, bodyTo, walkCtx{keyframes: ctx.keyframes, depth: ctx.depth + 1})
		case declListAtRules[name]:
			c.decls(bodyFrom, bodyTo, declOpts{depth: ctx.depth + 1})
		default:
			// unknown at-rule: block contents are not analysed (documented heuristic)
		}
	}
	if closeIdx+1 > i {
		return closeIdx + 1
	}
	return i + 1
}

// atRuleName lowercases "@Media" and strips a vendor prefix: "@-webkit-
// keyframes" → "keyframes".
func atRuleName(data string) string {
	n := strings.ToLower(strings.TrimPrefix(data, "@"))
	if strings.HasPrefix(n, "-") {
		if k := strings.Index(n[1:], "-"); k >= 0 {
			n = n[k+2:]
		}
	}
	return n
}

// qualifiedRule parses "selector { declarations }".
func (c *cssChecker) qualifiedRule(i, to int, ctx walkCtx) int {
	// find '{' at level 0
	j := i
	level := 0
	brace := -1
	for j < to {
		tt := c.toks[j].tt
		if tt == css.LeftBraceToken && level == 0 {
			brace = j
			break
		}
		level += depthDelta(tt)
		if level < 0 {
			level = 0
		}
		j++
	}
	if brace < 0 {
		// Malformed (no block): analyse the remainder as a selector so obvious
		// problems still surface.
		c.checkSelector(i, to, ctx)
		return to
	}
	sum := c.checkSelector(i, brace, ctx)
	closeIdx := c.matchingBrace(brace, to)
	if ctx.depth >= maxCSSDepth {
		return closeIdx + 1 // too deep: skip the body but keep positions
	}
	c.decls(brace+1, closeIdx, declOpts{rootRule: sum.rootRule, stageTarget: sum.hitsRoot, depth: ctx.depth + 1})
	return closeIdx + 1
}

// decls parses a declaration list (with CSS-nesting support).
func (c *cssChecker) decls(from, to int, opts declOpts) {
	if to > len(c.toks) {
		to = len(c.toks)
	}
	i := from
	for i < to {
		var next int
		switch c.toks[i].tt {
		case css.WhitespaceToken, css.CommentToken, css.SemicolonToken, css.CommaToken,
			css.RightBraceToken, css.RightParenthesisToken, css.RightBracketToken,
			css.CDOToken, css.CDCToken:
			next = i + 1
		case css.AtKeywordToken:
			next = c.atRule(i, to, walkCtx{depth: opts.depth})
		case css.LeftBraceToken:
			next = i + 1 // stray brace
		default:
			next = c.declaration(i, to, opts)
		}
		if next <= i {
			next = i + 1
		}
		i = next
	}
}

// declaration parses one "prop: value" (or a nested rule) starting at i.
// Returns the index just past it.
func (c *cssChecker) declaration(i, to int, opts declOpts) int {
	// scan to the terminator at level 0: ';', '{' or '}'
	j := i
	level := 0
	term, termTT := -1, css.ErrorToken
	for j < to {
		tt := c.toks[j].tt
		if level == 0 && (tt == css.SemicolonToken || tt == css.LeftBraceToken || tt == css.RightBraceToken) {
			term, termTT = j, tt
			break
		}
		level += depthDelta(tt)
		j++
	}

	if termTT == css.LeftBraceToken {
		// nested rule (CSS nesting)
		closeIdx := c.matchingBrace(term, to)
		if opts.depth < maxCSSDepth {
			sum := c.checkSelector(i, term, walkCtx{})
			c.decls(term+1, closeIdx, declOpts{
				rootRule:    sum.rootRule,
				stageTarget: sum.hitsRoot,
				depth:       opts.depth + 1,
			})
		}
		return closeIdx + 1
	}
	if term < 0 {
		term, termTT = to, css.ErrorToken
	}

	// declaration [i, term)
	next := term
	switch termTT {
	case css.SemicolonToken:
		next = term + 1
	case css.ErrorToken:
		next = to
	}
	// RightBrace → next == term (the caller skips the brace)

	p := i
	for p < term && (c.toks[p].tt == css.WhitespaceToken || c.toks[p].tt == css.CommentToken) {
		p++
	}
	if p >= term {
		return next
	}
	pt := c.toks[p]
	if pt.tt != css.IdentToken && pt.tt != css.CustomPropertyNameToken {
		return next // not a declaration
	}
	q := p + 1
	for q < term && (c.toks[q].tt == css.WhitespaceToken || c.toks[q].tt == css.CommentToken) {
		q++
	}
	if q >= term || c.toks[q].tt != css.ColonToken {
		return next // malformed: no colon
	}
	prop := strings.ToLower(pt.data)
	valFrom := q + 1

	if opts.rootRule && !strings.HasPrefix(prop, "--") {
		c.add(CodeRootTokens, SeverityError, pt.start,
			":root rule declares \""+truncate(prop, 40)+"\": :root may only declare --* custom properties")
	}

	if opts.stageTarget {
		if prop == "position" && valueHasIdent(c.toks, valFrom, term, "fixed") {
			c.add(CodeStageWrapper, SeverityWarning, pt.start,
				"root element uses position: fixed: the canvas owns the stage — use static/relative layout")
		}
		if viewportSizedProp(prop) {
			if unit := firstViewportUnit(c.toks, valFrom, term); unit != "" {
				c.add(CodeStageWrapper, SeverityWarning, pt.start,
					"root element sizes itself to the viewport ("+unit+"): the canvas owns the stage — size the content instead")
			}
		}
	}

	// external resources in url(...)
	for k := valFrom; k < term; k++ {
		target, ok := c.urlAt(k, term)
		if !ok || target == "" {
			continue
		}
		if !externalResourceOK(target) {
			c.add(CodeExternalResource, SeverityWarning, c.toks[k].start,
				"CSS url("+truncate(target, 60)+") does not load: external and relative URLs are blocked — use a data: URI or a Google Fonts URL")
		}
	}
	return next
}

func viewportSizedProp(prop string) bool {
	switch prop {
	case "width", "height", "min-width", "min-height":
		return true
	}
	return false
}

func valueHasIdent(toks []cssTok, from, to int, want string) bool {
	for i := from; i < to && i < len(toks); i++ {
		if toks[i].tt == css.IdentToken && strings.EqualFold(toks[i].data, want) {
			return true
		}
	}
	return false
}

func isViewportUnit(d string) bool {
	switch strings.ToLower(d) {
	case "100vw", "100vh", "100dvh":
		return true
	}
	return false
}

func firstViewportUnit(toks []cssTok, from, to int) string {
	for i := from; i < to && i < len(toks); i++ {
		if toks[i].tt == css.DimensionToken && isViewportUnit(toks[i].data) {
			return strings.ToLower(toks[i].data)
		}
	}
	return ""
}

// urlAt extracts the target of a url(...) at token index i, if any.
func (c *cssChecker) urlAt(i, to int) (string, bool) {
	t := c.toks[i]
	switch t.tt {
	case css.URLToken:
		return urlInner(t.data), true
	case css.BadURLToken:
		inner := urlInner(t.data)
		return inner, inner != ""
	case css.FunctionToken:
		if !strings.EqualFold(strings.TrimSuffix(t.data, "("), "url") {
			return "", false
		}
		var b strings.Builder
		for k := i + 1; k < to && k < len(c.toks); k++ {
			if c.toks[k].tt == css.RightParenthesisToken {
				break
			}
			b.WriteString(c.toks[k].data)
		}
		return strings.TrimSpace(unquoteString(strings.TrimSpace(b.String()))), true
	}
	return "", false
}

// checkImport enforces @import → fonts.googleapis.com.
func (c *cssChecker) checkImport(from, to int) {
	rel := 0
	if from < len(c.toks) {
		rel = c.toks[from].start
	}
	u, ok := c.extractURL(from, to)
	if !ok {
		c.add(CodeCSSImport, SeverityError, rel,
			"@import without a URL: only https://fonts.googleapis.com font CSS may be imported")
		return
	}
	if !isGoogleFontsURL(u, "fonts.googleapis.com") {
		c.add(CodeCSSImport, SeverityError, rel,
			"@import \""+truncate(u, 80)+"\" is not allowed: only https://fonts.googleapis.com font CSS may be imported")
	}
}

func (c *cssChecker) extractURL(from, to int) (string, bool) {
	for i := from; i < to && i < len(c.toks); i++ {
		t := c.toks[i]
		switch {
		case t.tt == css.URLToken:
			return urlInner(t.data), true
		case t.tt == css.StringToken:
			return unquoteString(t.data), true
		case t.tt == css.FunctionToken && strings.EqualFold(strings.TrimSuffix(t.data, "("), "url"):
			for k := i + 1; k < to && k < len(c.toks); k++ {
				if c.toks[k].tt == css.RightParenthesisToken {
					break
				}
				if c.toks[k].tt == css.StringToken {
					return unquoteString(c.toks[k].data), true
				}
			}
			return "", true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// selectors

// checkSelector analyses every selector part of a rule prelude and reports
// the selector-level rules. It also summarises whether the rule targets :root
// or the component root element.
func (c *cssChecker) checkSelector(from, to int, ctx walkCtx) selSummary {
	var sum selSummary
	if ctx.keyframes || from >= to {
		return sum
	}
	for _, p := range splitTopLevel(c.toks, from, to, css.CommaToken) {
		if p.from >= p.to {
			continue
		}
		info := c.analyzePart(p.from, p.to, 0)

		if info.subjectIsRoot {
			sum.rootRule = true
			continue // :root rules are governed by root-tokens-only
		}
		if info.subjectHitsRoot(c.rootClass, c.rootID) {
			sum.hitsRoot = true
		}
		if info.htmlBody {
			c.add(CodeRootSelectors, SeverityError, info.startOff,
				"selector \""+truncate(info.text, 120)+"\" targets html/body: the canvas owns the document — scope the selector to the component root")
			continue
		}
		if info.universal {
			c.add(CodeUniversalReset, SeverityError, info.startOff,
				"selector \""+truncate(info.text, 120)+"\" is a global reset: scope it under the component root (e.g. \".c1-card *\")")
			continue
		}
		c.checkAnchored(info)
	}
	return sum
}

func (c *cssChecker) checkAnchored(info selInfo) {
	if info.hasAmp {
		return // "&" anchors the rule to its (already scoped) parent rule
	}
	anchor := ""
	for _, id := range info.ids {
		if !c.markupID[id] {
			anchor = "#" + id
			break
		}
	}
	if anchor == "" {
		for _, cls := range info.classes {
			if !c.markupClass[cls] {
				anchor = "." + cls
				break
			}
		}
	}

	if len(info.classes) == 0 && len(info.ids) == 0 {
		c.add(CodeScopedCSS, SeverityError, info.startOff,
			"selector \""+truncate(info.text, 120)+"\" has no class or id: anchor every rule on a class from the markup (e.g. \"h3.c1-title\")")
		return
	}
	if anchor != "" {
		c.add(CodeScopedCSS, SeverityWarning, info.startOff,
			"selector \""+truncate(info.text, 120)+"\" uses \""+anchor+"\", which is not in the markup: add it to an element or drop the rule")
	}
}

type selInfo struct {
	text          string
	startOff      int // fragment-relative offset of the first token
	classes       []string
	ids           []string
	types         []string
	subject       compInfo
	subjectIsRoot bool
	htmlBody      bool
	universal     bool
	hasAmp        bool // "&" nesting selector present
}

type compInfo struct {
	base        string // "" (implicit) | type selector | "*"
	classes     []string
	ids         []string
	hasAttr     bool
	firstIsRoot bool // compound begins with the :root pseudo-class
}

func (i selInfo) subjectHitsRoot(rootClass, rootID map[string]bool) bool {
	for _, cls := range i.subject.classes {
		if rootClass[cls] {
			return true
		}
	}
	for _, id := range i.subject.ids {
		if rootID[id] {
			return true
		}
	}
	return false
}

// analyzePart parses one selector part (no top-level commas) into a selInfo.
// depth bounds recursion into selector-taking pseudo-functions.
func (c *cssChecker) analyzePart(from, to int, depth int) selInfo {
	if to > len(c.toks) {
		to = len(c.toks)
	}
	info := selInfo{}
	if from >= to {
		return info
	}
	info.text = joinTokens(c.toks, from, to)
	info.startOff = c.toks[from].start
	if depth > maxCSSDepth {
		return info // stop recursing; still report what we saw
	}

	var comps []compInfo
	i := from
	for i < to {
		t := c.toks[i]
		if t.tt == css.WhitespaceToken || isCombinator(t) {
			i++
			continue
		}
		start := i
		var comp compInfo
		first := true
	compound:
		for i < to {
			t := c.toks[i]
			switch {
			case t.tt == css.WhitespaceToken || isCombinator(t):
				break compound
			case t.tt == css.DelimToken && t.data == "*":
				if first {
					comp.base = "*"
				}
				first, i = false, i+1
			case t.tt == css.DelimToken && t.data == "&":
				info.hasAmp = true
				first, i = false, i+1
			case t.tt == css.IdentToken:
				if first {
					comp.base = t.data
					info.types = append(info.types, t.data)
					if strings.EqualFold(t.data, "html") || strings.EqualFold(t.data, "body") {
						info.htmlBody = true
					}
				}
				first, i = false, i+1
			case t.tt == css.HashToken:
				id := strings.TrimPrefix(t.data, "#")
				comp.ids = append(comp.ids, id)
				info.ids = append(info.ids, id)
				first, i = false, i+1
			case t.tt == css.DelimToken && t.data == ".":
				if i+1 < to && c.toks[i+1].tt == css.IdentToken {
					cls := c.toks[i+1].data
					comp.classes = append(comp.classes, cls)
					info.classes = append(info.classes, cls)
					i += 2
				} else {
					i++
				}
				first = false
			case t.tt == css.LeftBracketToken:
				comp.hasAttr = true
				i = skipToMatching(c.toks, i, to, css.LeftBracketToken, css.RightBracketToken)
				first = false
			case t.tt == css.ColonToken:
				i++
				if i < to && c.toks[i].tt == css.ColonToken { // pseudo-element
					i++
				}
				if i >= to {
					break compound
				}
				pt := c.toks[i]
				switch pt.tt {
				case css.IdentToken:
					if first && strings.EqualFold(pt.data, "root") {
						comp.firstIsRoot = true
					}
					first, i = false, i+1
				case css.FunctionToken:
					fname := strings.ToLower(strings.TrimSuffix(pt.data, "("))
					closeIdx := matchingParen(c.toks, i, to)
					if selectorPseudos[fname] && depth < maxCSSDepth {
						sub := c.analyzePart(i+1, closeIdx, depth+1)
						info.classes = append(info.classes, sub.classes...)
						info.ids = append(info.ids, sub.ids...)
						info.types = append(info.types, sub.types...)
						if sub.htmlBody {
							info.htmlBody = true
						}
						if sub.hasAmp {
							info.hasAmp = true
						}
					}
					first, i = false, closeIdx+1
				default:
					first, i = false, i+1
				}
			default:
				break compound
			}
		}
		if i == start {
			i++ // guarantee progress on unexpected tokens
		}
		comps = append(comps, comp)
	}

	if len(comps) > 0 {
		info.subject = comps[len(comps)-1]
		info.subjectIsRoot = info.subject.firstIsRoot
		info.universal = (info.subject.base == "*" || info.subject.base == "") &&
			!info.subject.hasAttr && len(info.subject.classes) == 0 && len(info.subject.ids) == 0 &&
			!info.hasAmp &&
			len(info.classes) == 0 && len(info.ids) == 0 && len(info.types) == 0
	}
	return info
}

// ---------------------------------------------------------------------------
// token helpers

func depthDelta(tt css.TokenType) int {
	switch tt {
	case css.FunctionToken, css.LeftParenthesisToken, css.LeftBracketToken:
		return 1
	case css.RightParenthesisToken, css.RightBracketToken:
		return -1
	}
	return 0
}

func isCombinator(t cssTok) bool {
	return t.tt == css.DelimToken && (t.data == ">" || t.data == "+" || t.data == "~")
}

type span struct{ from, to int }

// splitTopLevel splits [from,to) on separator tokens at paren/bracket level 0.
func splitTopLevel(toks []cssTok, from, to int, sep css.TokenType) []span {
	if to > len(toks) {
		to = len(toks)
	}
	var out []span
	start, level := from, 0
	for i := from; i < to; i++ {
		tt := toks[i].tt
		if tt == sep && level == 0 {
			out = append(out, span{start, i})
			start = i + 1
			continue
		}
		level += depthDelta(tt)
		if level < 0 {
			level = 0
		}
	}
	return append(out, span{start, to})
}

// skipToMatching returns the index just past the token closing the delimiter
// at i (or to when unbalanced).
func skipToMatching(toks []cssTok, i, to int, open, close css.TokenType) int {
	if to > len(toks) {
		to = len(toks)
	}
	level := 0
	for j := i; j < to; j++ {
		switch toks[j].tt {
		case open:
			level++
		case close:
			level--
			if level == 0 {
				return j + 1
			}
		}
	}
	return to
}

// matchingParen returns the index of the ')' closing the function token at i,
// or to when unbalanced.
func matchingParen(toks []cssTok, i, to int) int {
	if to > len(toks) {
		to = len(toks)
	}
	level := 0
	for j := i; j < to; j++ {
		tt := toks[j].tt
		switch {
		case tt == css.FunctionToken || tt == css.LeftParenthesisToken:
			level++
		case tt == css.RightParenthesisToken:
			level--
			if level == 0 {
				return j
			}
		}
	}
	return to // unbalanced: exclusive end
}

// matchingBrace returns the index of the '}' closing the '{' at i, or to.
func (c *cssChecker) matchingBrace(i, to int) int {
	if to > len(c.toks) {
		to = len(c.toks)
	}
	level := 0
	for j := i; j < to; j++ {
		switch c.toks[j].tt {
		case css.LeftBraceToken:
			level++
		case css.RightBraceToken:
			level--
			if level == 0 {
				return j
			}
		}
	}
	return to // unbalanced: body runs to the end
}

func joinTokens(toks []cssTok, from, to int) string {
	var b strings.Builder
	for i := from; i < to && i < len(toks); i++ {
		b.WriteString(toks[i].data)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func unquoteString(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// urlInner strips the url( ... ) wrapper (and quotes) from a URLToken.
func urlInner(data string) string {
	s := strings.TrimSpace(data)
	if len(s) >= 4 && strings.EqualFold(s[:4], "url(") {
		s = s[4:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), ")")
	return strings.TrimSpace(unquoteString(strings.TrimSpace(s)))
}

func stringSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, v := range list {
		if v != "" {
			m[v] = true
		}
	}
	return m
}
