package component

import (
	"fmt"
	"sort"
	"strings"
)

// maxFragmentBytes is the size limit for a fragment (200 KiB).
const maxFragmentBytes = 200 * 1024

// Violation is one rule finding. Line is 1-based; 0 means "unknown" (input too
// large, layout-phase findings, validator failure). Snippet is the offending
// source line (truncated) or, for layout findings, a compact fact summary.
type Violation struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Line     int      `json:"line"`
	Snippet  string   `json:"snippet,omitempty"`
}

// Fragment describes what was found in the markup. It is returned with every
// Report so callers (tools, critics) can see the parsed shape without
// re-parsing.
type Fragment struct {
	Bytes       int      `json:"bytes"`
	RootTag     string   `json:"root_tag,omitempty"` // tag of the single root element; "" when there is not exactly one
	Links       []string `json:"links,omitempty"`    // href of each <link>, in document order
	StyleBlocks int      `json:"style_blocks"`       // number of <style> elements anywhere
	Classes     []string `json:"classes,omitempty"`  // sorted unique class tokens from the markup
	IDs         []string `json:"ids,omitempty"`      // sorted unique ids from the markup
}

// Report is the result of Validate.
type Report struct {
	Violations []Violation `json:"violations"`
	Fragment   Fragment    `json:"fragment"`
}

// OK reports whether the fragment may be rendered: no error-severity
// violations. Warnings do not block rendering.
func (r *Report) OK() bool {
	if r == nil {
		return false
	}
	for _, v := range r.Violations {
		if v.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Format renders violations as compact, LLM-friendly text, one per line:
//
//	error no-script (line 12): <script> is not allowed: ...
//	warning external-resource (line 3): ...
//
// Findings without a line number omit the "(line N)" part.
func Format(vs []Violation) string {
	var b strings.Builder
	for i, v := range vs {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case v.Line > 0:
			fmt.Fprintf(&b, "%s %s (line %d): %s", v.Severity, v.Code, v.Line, v.Message)
		default:
			fmt.Fprintf(&b, "%s %s: %s", v.Severity, v.Code, v.Message)
		}
	}
	return b.String()
}

// Validate checks a generator-written HTML fragment. It never panics: if the
// validator itself fails on the input it fails closed with a single
// internal-error violation.
//
// Findings are grouped: markup findings in token order, then style-block and
// single-root structural findings, then CSS findings per <style> block in
// document order.
func Validate(src string) (rep *Report) {
	defer func() {
		if rec := recover(); rec != nil {
			rep = &Report{
				Violations: []Violation{{
					Code:     CodeInternal,
					Severity: SeverityError,
					Message: fmt.Sprintf("validator failed to analyse this input (%s): simplify the markup and retry",
						truncate(fmt.Sprintf("%v", rec), 120)),
				}},
				Fragment: Fragment{Bytes: len(src)},
			}
		}
	}()

	if len(src) > maxFragmentBytes {
		return &Report{
			Violations: []Violation{{
				Code:     CodeTooLarge,
				Severity: SeverityError,
				Message: fmt.Sprintf("fragment is %d KB; the limit is %d KB — split the component or simplify the markup",
					len(src)/1024, maxFragmentBytes/1024),
			}},
			Fragment: Fragment{Bytes: len(src)},
		}
	}

	lines := newLineIndex(src)
	s := scanFragment(src, lines)

	vs := make([]Violation, 0, 8)
	vs = append(vs, s.inline...)
	vs = append(vs, s.structural...)
	vs = append(vs, s.rootViolations...)

	for _, b := range s.styleBlocks {
		vs = append(vs, checkStyleBlock(lines, s, b)...)
	}
	vs = append(vs, checkRootInline(lines, s)...)

	return &Report{Violations: vs, Fragment: s.fragment}
}

// lineIndex maps byte offsets to 1-based line numbers of src.
type lineIndex struct {
	src    string
	starts []int // byte offsets of each '\n'
}

func newLineIndex(src string) *lineIndex {
	starts := make([]int, 0, 1+strings.Count(src, "\n"))
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i)
		}
	}
	return &lineIndex{src: src, starts: starts}
}

// lineAt returns the 1-based line containing the given byte offset.
func (l *lineIndex) lineAt(off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(l.src) {
		off = len(l.src)
	}
	// number of '\n' strictly before off, plus one
	n := sort.Search(len(l.starts), func(i int) bool { return l.starts[i] >= off })
	return n + 1
}

// snippetAt returns the trimmed source line containing off, truncated.
func (l *lineIndex) snippetAt(off int) string {
	if off < 0 {
		off = 0
	}
	if off > len(l.src) {
		off = len(l.src)
	}
	start := strings.LastIndexByte(l.src[:off], '\n') + 1
	end := strings.IndexByte(l.src[off:], '\n')
	if end < 0 {
		end = len(l.src)
	} else {
		end += off
	}
	return truncate(strings.TrimSpace(l.src[start:end]), 160)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func newViolation(code string, sev Severity, line int, snippet, msg string) Violation {
	return Violation{Code: code, Severity: sev, Message: msg, Line: line, Snippet: snippet}
}
