package component

import (
	"io"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Element groups that only exist to describe the document shell.
var documentTags = map[string]bool{
	"html": true, "head": true, "body": true, "meta": true, "title": true, "base": true,
}

// Nested-document elements (plus the ones the catalog promises to reject).
var iframeTags = map[string]bool{
	"iframe": true, "frame": true, "frameset": true, "object": true,
	"embed": true, "portal": true, "fencedframe": true, "applet": true,
}

// HTML void elements: they never open a nesting level.
var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// Attributes whose value is a URL; javascript:/vbscript: in any of them is a
// script vector.
var urlAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "poster": true,
	"data": true, "xlink:href": true, "background": true, "cite": true,
	"longdesc": true, "usemap": true, "manifest": true, "profile": true,
	"codebase": true, "classid": true, "code": true, "archive": true,
	"srcset": true, "ping": true, "dynsrc": true, "lowsrc": true,
}

// Elements whose media sources are checked for external-resource.
var mediaSrcAttrs = map[string][]string{
	"img":    {"src"},
	"video":  {"src", "poster"},
	"source": {"src"},
	"audio":  {"src"},
	"track":  {"src"},
	"input":  {"src"},
	"image":  {"href", "xlink:href"}, // SVG
	"use":    {"href", "xlink:href"}, // SVG
}

// SVG animation elements can inject handlers via attributeName.
var svgAnimTags = map[string]bool{
	"set": true, "animate": true, "animatemotion": true, "animatetransform": true,
}

type styleBlock struct {
	openLine int // line of the <style> start tag
	closed   bool
	content  string // raw text between <style> and </style>
	cStart   int    // absolute offset of content within the fragment
}

type topElem struct {
	tag  string
	line int
	off  int
}

// scanResult is the output of the markup pass.
type scanResult struct {
	fragment Fragment
	// inline holds findings emitted in token order (document wrapper, script,
	// iframe, font links).
	inline []Violation
	// structural holds findings that need the whole scan: style-block and
	// single-root counts, link count.
	structural []Violation
	// rootViolations: single-root findings (stray text, element count).
	rootViolations []Violation

	styleBlocks []styleBlock
	topElems    []topElem
	classSet    map[string]bool
	idSet       map[string]bool
	rootAttrs   []html.Attribute // attrs of the single root element (nil if not exactly one root)
	rootTag     string
	rootOff     int // byte offset of the root element's start tag
	linkCount   int
	linkLines   []int // line of each <link>, in order
}

func scanFragment(src string, lines *lineIndex) *scanResult {
	s := &scanResult{
		classSet: map[string]bool{},
		idSet:    map[string]bool{},
	}
	s.fragment.Bytes = len(src)

	z := html.NewTokenizer(strings.NewReader(src))
	off := 0
	var stack []string
	foreign := 0         // depth inside <svg>/<math>
	var openStyles []int // indices into styleBlocks of unclosed <style>s
	pendingStyle := -1   // index into styleBlocks awaiting its content text
	strays := []Violation{}

	for {
		tt := z.Next()
		n := len(z.Raw())
		tokStart := off
		off += n

		if tt == html.ErrorToken {
			if err := z.Err(); err != nil && err != io.EOF {
				s.inline = append(s.inline, newViolation(
					CodeInternal, SeverityError, lines.lineAt(tokStart), lines.snippetAt(tokStart),
					"markup could not be tokenized; simplify the markup and retry"))
			}
			break
		}

		// If a <style> start tag was just seen, the next token (if text) is
		// its content.
		if pendingStyle >= 0 {
			if tt == html.TextToken {
				b := &s.styleBlocks[pendingStyle]
				b.cStart = tokStart
				b.content = src[tokStart : tokStart+n]
			}
			pendingStyle = -1
		}

		line := lines.lineAt(tokStart)

		switch tt {
		case html.DoctypeToken:
			s.inline = append(s.inline, newViolation(
				CodeDocumentWrapper, SeverityError, line, lines.snippetAt(tokStart),
				"<!doctype> found: send only the component fragment (optional <link>s, one <style>, one root element); the canvas builds the document"))

		case html.TextToken:
			if len(stack) == 0 && strings.TrimSpace(string(z.Text())) != "" {
				strays = append(strays, newViolation(
					CodeSingleRoot, SeverityError, line, lines.snippetAt(tokStart),
					"stray top-level text: only one root element, whitespace and comments may appear at the top level"))
			}

		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token() // Data lowercased, attr keys lowercased, attr values unescaped
			tag := tok.Data
			selfClosing := tt == html.SelfClosingTagToken
			atTop := len(stack) == 0

			// --- document wrapper -------------------------------------------------
			if documentTags[tag] && !(tag == "title" && foreign > 0) {
				s.inline = append(s.inline, newViolation(
					CodeDocumentWrapper, SeverityError, line, lines.snippetAt(tokStart),
					documentWrapperMessage(tag)))
			}

			// --- script ------------------------------------------------------------
			if tag == "script" {
				s.inline = append(s.inline, newViolation(
					CodeScript, SeverityError, line, lines.snippetAt(tokStart),
					"<script> is not allowed: the preview runs no JavaScript — use inline SVG or CSS instead"))
			} else if tag == "noscript" {
				s.inline = append(s.inline, newViolation(
					CodeScript, SeverityError, line, lines.snippetAt(tokStart),
					"<noscript> is not allowed: the preview runs no JavaScript — remove the element"))
			}

			// --- nested documents --------------------------------------------------
			if iframeTags[tag] {
				s.inline = append(s.inline, newViolation(
					CodeIframe, SeverityError, line, lines.snippetAt(tokStart),
					"<"+tag+"> is not allowed: nested documents break auto-height — use inline SVG, <img> or <video>"))
			}

			// --- attributes --------------------------------------------------------
			for _, a := range tok.Attr {
				key := a.Key // already lowercased by the tokenizer
				val := a.Val

				if len(key) > 2 && strings.HasPrefix(key, "on") {
					s.inline = append(s.inline, newViolation(
						CodeScript, SeverityError, line, lines.snippetAt(tokStart),
						"\""+key+"\" event handler on <"+tag+"> is not allowed: the preview runs no JavaScript — remove the attribute"))
				}

				if urlAttrs[key] && isScriptURL(val) {
					s.inline = append(s.inline, newViolation(
						CodeScript, SeverityError, line, lines.snippetAt(tokStart),
						"\""+key+"=\" URL on <"+tag+"> is a script URL ("+truncate(val, 60)+"): not allowed — the preview runs no JavaScript"))
				}

				if key == "attributename" && svgAnimTags[tag] &&
					len(val) > 2 && strings.HasPrefix(strings.ToLower(val), "on") {
					s.inline = append(s.inline, newViolation(
						CodeScript, SeverityError, line, lines.snippetAt(tokStart),
						"<"+tag+" attributeName=\""+val+"\"> would inject an event handler: not allowed — remove the element"))
				}
			}

			// --- media sources (external-resource) --------------------------------
			if want, ok := mediaSrcAttrs[tag]; ok {
				for _, a := range tok.Attr {
					if !containsString(want, a.Key) {
						continue
					}
					if !externalResourceOK(a.Val) {
						s.inline = append(s.inline, newViolation(
							CodeExternalResource, SeverityWarning, line, lines.snippetAt(tokStart),
							"<"+tag+" "+a.Key+">=\""+truncate(a.Val, 60)+"\" does not load: external and relative URLs are blocked — use a data: URI"))
					}
				}
			}

			// --- <link> ------------------------------------------------------------
			if tag == "link" {
				s.linkCount++
				s.linkLines = append(s.linkLines, line)
				href, rel := attrVal(tok.Attr, "href"), attrVal(tok.Attr, "rel")
				s.fragment.Links = append(s.fragment.Links, href)
				if msg, bad := linkProblem(href, rel); bad {
					s.inline = append(s.inline, newViolation(
						CodeFontLinks, SeverityError, line, lines.snippetAt(tokStart), msg))
				}
			}

			// --- <style> -----------------------------------------------------------
			if tag == "style" {
				s.styleBlocks = append(s.styleBlocks, styleBlock{openLine: line, closed: false})
				pendingStyle = len(s.styleBlocks) - 1
				openStyles = append(openStyles, len(s.styleBlocks)-1)
			}

			// --- class/id collection ----------------------------------------------
			if v, ok := attrVal2(tok.Attr, "class"); ok {
				for _, c := range strings.Fields(v) {
					s.classSet[c] = true
				}
			}
			if v, ok := attrVal2(tok.Attr, "id"); ok && v != "" {
				s.idSet[v] = true
			}

			// --- top-level element bookkeeping ------------------------------------
			if atTop && tag != "link" && tag != "style" {
				s.topElems = append(s.topElems, topElem{tag: tag, line: line, off: tokStart})
				if len(s.topElems) == 1 {
					s.rootAttrs = tok.Attr
					s.rootOff = tokStart
				}
			}

			// --- depth management --------------------------------------------------
			// HTML treats "<div/>" as an opening tag (the solidus is ignored),
			// so a self-closing non-foreign element stays open; foreign content
			// (SVG/MathML) honours self-closing, and void elements never open.
			if !voidTags[tag] && !(selfClosing && foreign > 0) {
				stack = append(stack, tag)
				if tag == "svg" || tag == "math" {
					foreign++
				}
			}

		case html.EndTagToken:
			tok := z.Token()
			tag := tok.Data
			if voidTags[tag] {
				break // stray end tag for a void element: ignored, like the parser does
			}
			// Pop through the matching open element (HTML recovery: an end tag
			// closes all deeper elements too).
			idx := -1
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == tag {
					idx = i
					break
				}
			}
			if idx >= 0 {
				for _, t := range stack[idx:] {
					if t == "svg" || t == "math" {
						foreign--
					}
				}
				stack = stack[:idx]
			}
			if tag == "style" && len(openStyles) > 0 {
				s.styleBlocks[openStyles[len(openStyles)-1]].closed = true
				openStyles = openStyles[:len(openStyles)-1]
			}
		}
	}

	// unclosed <style> blocks
	for i := range s.styleBlocks {
		b := &s.styleBlocks[i]
		if !b.closed {
			s.structural = append(s.structural, newViolation(
				CodeStyleBlock, SeverityError, b.openLine, lines.snippetAt(blockOpenOffset(lines, b)),
				"<style> opened here is never closed — add </style>"))
		}
	}
	// duplicate <style> blocks
	if len(s.styleBlocks) > 1 {
		linesList := make([]string, 0, len(s.styleBlocks))
		for i := range s.styleBlocks {
			linesList = append(linesList, itoa(s.styleBlocks[i].openLine))
		}
		s.structural = append(s.structural, newViolation(
			CodeStyleBlock, SeverityError, s.styleBlocks[1].openLine,
			lines.snippetAt(blockOpenOffset(lines, &s.styleBlocks[1])),
			itoa(len(s.styleBlocks))+" <style> blocks found (lines "+strings.Join(linesList, ", ")+
				"): at most one <style> block is allowed — merge them into one block"))
	}
	// too many <link>s
	if s.linkCount > 4 {
		s.structural = append(s.structural, newViolation(
			CodeFontLinks, SeverityError, s.linkLines[4], lines.snippetAt(linkOff(lines, s.linkLines[4])),
			itoa(s.linkCount)+" <link> tags found: at most 4 are allowed — drop the extra font links"))
	}

	// single-root: stray text first, then element count
	s.rootViolations = append(s.rootViolations, strays...)
	switch len(s.topElems) {
	case 0:
		s.rootViolations = append(s.rootViolations, newViolation(
			CodeSingleRoot, SeverityError, 0, "",
			"no root element found: the fragment must contain exactly one component element (plus optional <link>s and one <style>)"))
	case 1:
		s.rootTag = s.topElems[0].tag
	default:
		parts := make([]string, 0, len(s.topElems))
		for _, e := range s.topElems {
			parts = append(parts, "<"+e.tag+"> at line "+itoa(e.line))
		}
		s.rootViolations = append(s.rootViolations, newViolation(
			CodeSingleRoot, SeverityError, s.topElems[0].line, lines.snippetAt(s.topElems[0].off),
			itoa(len(s.topElems))+" top-level elements ("+strings.Join(parts, ", ")+
				"): exactly one component element is allowed — wrap them in a single root element"))
	}

	// fragment summary
	s.fragment.RootTag = s.rootTag
	s.fragment.StyleBlocks = len(s.styleBlocks)
	s.fragment.Classes = sortedKeys(s.classSet)
	s.fragment.IDs = sortedKeys(s.idSet)

	return s
}

// lineStartOff returns the byte offset where the given 1-based line begins.
func lineStartOff(lines *lineIndex, line int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(lines.src[off:], '\n')
		if i < 0 {
			break
		}
		off += i + 1
	}
	return off
}

// linkOff locates a <link> tag on the given line (for snippets).
func linkOff(lines *lineIndex, line int) int {
	start := lineStartOff(lines, line)
	if i := strings.Index(lines.src[start:], "<link"); i >= 0 {
		return start + i
	}
	return start
}

// blockOpenOffset reconstructs the offset of a <style> start tag from its
// content start; for empty/unclosed blocks it falls back to the line start.
func blockOpenOffset(lines *lineIndex, b *styleBlock) int {
	if b.cStart > 0 {
		// search backwards from content start for "<style"
		i := strings.LastIndex(lines.src[:min(b.cStart, len(lines.src))], "<style")
		if i >= 0 {
			return i
		}
	}
	return lineStartOff(lines, b.openLine)
}

func documentWrapperMessage(tag string) string {
	switch tag {
	case "title":
		return "<title> belongs to the document head: send only the component fragment — the canvas builds the document"
	case "meta", "base":
		return "<" + tag + "> is a document head element: send only the component fragment — the canvas builds the document"
	default:
		return "<" + tag + "> is a document element: send only the component fragment (optional <link>s, one <style>, one root element); the canvas builds the document"
	}
}

// isScriptURL reports whether a URL attribute value is a javascript:/vbscript:
// URL. HTML entities are already decoded by the tokenizer; ASCII tab/newline/
// carriage-return/formfeed are stripped everywhere (browsers do the same) and
// leading/trailing whitespace and control bytes are trimmed.
func isScriptURL(v string) bool {
	v = strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', '\f':
			return -1
		}
		return r
	}, v)
	v = strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
	l := strings.ToLower(v)
	return strings.HasPrefix(l, "javascript:") || strings.HasPrefix(l, "vbscript:")
}

// linkProblem reports whether a <link> violates font-links.
func linkProblem(href, rel string) (string, bool) {
	rels := strings.Fields(strings.ToLower(rel))
	hasStylesheet := containsString(rels, "stylesheet")
	hasPreconnect := containsString(rels, "preconnect")

	switch {
	case href == "":
		return "<link> without href: only Google Fonts links are allowed (rel=\"stylesheet\" or rel=\"preconnect\")", true
	case hasStylesheet:
		if !isGoogleFontsURL(href, "fonts.googleapis.com") {
			return "link rel=\"stylesheet\" href=\"" + truncate(href, 80) +
				"\" is not Google Fonts: only https://fonts.googleapis.com stylesheets are allowed", true
		}
	case hasPreconnect:
		if !isGoogleFontsURL(href, "fonts.googleapis.com") && !isGoogleFontsURL(href, "fonts.gstatic.com") {
			return "link rel=\"preconnect\" href=\"" + truncate(href, 80) +
				"\" is not Google Fonts: preconnect is only allowed for https://fonts.googleapis.com or https://fonts.gstatic.com", true
		}
	default:
		return "link rel=\"" + truncate(rel, 40) + "\" href=\"" + truncate(href, 80) +
			"\" is not a Google Fonts link: only rel=\"stylesheet\" (fonts.googleapis.com) or rel=\"preconnect\" (fonts.googleapis.com, fonts.gstatic.com) over https are allowed", true
	}
	return "", false
}

// isGoogleFontsURL reports whether s is an https URL on exactly host (no
// subdomain or suffix tricks, no other scheme, no credentials).
func isGoogleFontsURL(s, host string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Hostname() == host && u.User == nil
}

// externalResourceOK reports whether a media source URL will actually load:
// data: URIs, same-fragment references (#...), and Google Fonts hosts.
func externalResourceOK(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	if l == "" || strings.HasPrefix(l, "data:") || strings.HasPrefix(l, "#") {
		return true
	}
	return isGoogleFontsURL(v, "fonts.googleapis.com") || isGoogleFontsURL(v, "fonts.gstatic.com")
}

func attrVal(attrs []html.Attribute, key string) string {
	v, _ := attrVal2(attrs, key)
	return v
}

func attrVal2(attrs []html.Attribute, key string) (string, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
