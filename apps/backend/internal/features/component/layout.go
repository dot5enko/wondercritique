package component

import (
	"fmt"
	"strings"
)

// DefaultStageHeight is the stage height used when the renderer does not
// report one (LayoutFacts.StageHeight <= 0).
const DefaultStageHeight = 220.0

// layoutEpsilon absorbs sub-pixel rounding when comparing boxes.
const layoutEpsilon = 0.5

// LayoutFacts are renderer-supplied measurements of the mounted fragment,
// taken after the component is painted inside the canvas stage. They feed
// CheckLayout; Validate does not need them.
//
// Zero values mean "unknown" and the corresponding check is skipped —
// except StageHeight, where <= 0 selects DefaultStageHeight, and
// ContentHeight, which falls back to RootHeight.
type LayoutFacts struct {
	// StageWidth is the stage viewport width in px. 0 = unknown → overflow-x
	// and the "covers the whole stage" test are skipped.
	StageWidth float64 `json:"stage_width"`
	// StageHeight is the stage viewport height in px. <= 0 → 220 (DefaultStageHeight).
	StageHeight float64 `json:"stage_height"`
	// ContentWidth is the scroll width of the mounted content (document
	// scrollWidth). 0 → falls back to RootWidth.
	ContentWidth float64 `json:"content_width"`
	// ContentHeight is the scroll height of the mounted content
	// (document scrollHeight). <= 0 → falls back to RootHeight.
	ContentHeight float64 `json:"content_height"`
	// RootLeft/RootTop are the root element's border-box origin relative to
	// the stage's top-left corner, in px.
	RootLeft float64 `json:"root_left"`
	RootTop  float64 `json:"root_top"`
	// RootWidth/RootHeight are the root element's border-box size in px.
	RootWidth  float64 `json:"root_width"`
	RootHeight float64 `json:"root_height"`
	// RootPosition is the computed position of the root element:
	// "static", "relative", "absolute", "fixed" or "sticky".
	RootPosition string `json:"root_position"`
	// RootBackground is the computed background-color of the root element
	// (e.g. "#fff", "rgb(0, 0, 0)", "rgba(0, 0, 0, 0.5)", "" when
	// transparent/unknown).
	RootBackground string `json:"root_background"`
}

// CheckLayout evaluates the post-render rules against renderer measurements.
// Findings have Line 0 (no source line) and carry a fact summary in Snippet.
func CheckLayout(f LayoutFacts) []Violation {
	stageH := f.StageHeight
	if stageH <= 0 {
		stageH = DefaultStageHeight
	}
	stageW := f.StageWidth

	contentH := f.ContentHeight
	if contentH <= 0 {
		contentH = f.RootHeight
	}
	contentW := f.ContentWidth
	if contentW <= 0 {
		contentW = f.RootWidth
	}

	covers := false
	if stageW > 0 && f.RootWidth > 0 && f.RootHeight > 0 {
		covers = f.RootLeft <= layoutEpsilon && f.RootTop <= layoutEpsilon &&
			f.RootWidth >= stageW-layoutEpsilon && f.RootHeight >= stageH-layoutEpsilon
	}
	fixed := strings.EqualFold(strings.TrimSpace(f.RootPosition), "fixed")

	facts := fmt.Sprintf("root %gx%g at (%g,%g); stage %gx%g",
		f.RootWidth, f.RootHeight, f.RootLeft, f.RootTop, stageW, stageH)

	var vs []Violation

	if contentH > stageH+layoutEpsilon {
		vs = append(vs, newViolation(CodeStageHeight, SeverityError, 0, facts,
			fmt.Sprintf("content is %gpx tall but the stage is %gpx: reduce the content height or make it fit the stage",
				contentH, stageH)))
	}

	switch {
	case fixed && covers:
		vs = append(vs, newViolation(CodeStageWrapper, SeverityError, 0, facts,
			fmt.Sprintf("root element is position: fixed and its box (%gx%g) covers the whole stage %gx%g: the canvas owns the stage — use static/relative layout",
				f.RootWidth, f.RootHeight, stageW, stageH)))
	case fixed:
		vs = append(vs, newViolation(CodeStageWrapper, SeverityError, 0, facts,
			"root element is position: fixed: the canvas owns the stage — use static/relative layout"))
	case covers:
		vs = append(vs, newViolation(CodeStageWrapper, SeverityError, 0, facts,
			fmt.Sprintf("root box %gx%g at (%g,%g) covers the whole stage %gx%g: it must not fill the canvas",
				f.RootWidth, f.RootHeight, f.RootLeft, f.RootTop, stageW, stageH)))
	}

	if covers && opaqueBackground(f.RootBackground) {
		vs = append(vs, newViolation(CodeOpaqueStage, SeverityWarning, 0, facts,
			fmt.Sprintf("root spans the whole stage and paints an opaque background (%s): keep the stage transparent unless the component paints its own background",
				truncate(strings.TrimSpace(f.RootBackground), 40))))
	}

	if stageW > 0 && contentW > stageW+layoutEpsilon {
		vs = append(vs, newViolation(CodeOverflowX, SeverityError, 0, facts,
			fmt.Sprintf("content is %gpx wide but the stage is %gpx: nothing may be wider than the viewport",
				contentW, stageW)))
	}

	return vs
}

// opaqueBackground reports whether a computed background-color paints opaque
// pixels. Unknown-but-non-empty values are treated as opaque (the renderer
// normally passes a resolved color).
func opaqueBackground(bg string) bool {
	s := strings.ToLower(strings.TrimSpace(bg))
	switch s {
	case "", "transparent", "none", "initial", "inherit", "unset", "revert", "revert-layer":
		return false
	}
	// hex with alpha: #rgba / #rrggbbaa
	if strings.HasPrefix(s, "#") {
		switch len(s) {
		case 5: // #rgba
			return s[4] != '0'
		case 9: // #rrggbbaa
			return s[7:] != "00"
		}
		return true
	}
	// rgb()/rgba()/hsla(): alpha is the last component
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		inner := s[i+1 : len(s)-1]
		if k := strings.LastIndexByte(inner, ','); k >= 0 {
			alpha := strings.TrimSpace(inner[k+1:])
			if a, ok := parseAlpha(alpha); ok {
				return a > 0
			}
		}
		return true
	}
	return true
}

// parseAlpha parses "1", "0.5", "50%".
func parseAlpha(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	percent := strings.HasSuffix(s, "%")
	if percent {
		s = strings.TrimSuffix(s, "%")
	}
	var (
		whole  int
		frac   float64
		scale  = 0.1
		seenDP bool
		ok     bool
	)
	neg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '-' && i == 0:
			neg = true
		case c >= '0' && c <= '9':
			whole = whole*10 + int(c-'0')
			if seenDP {
				frac += float64(c-'0') * scale
				scale /= 10
			}
			ok = true
		case c == '.' && !seenDP:
			seenDP = true
		default:
			return 0, false
		}
	}
	if !ok {
		return 0, false
	}
	v := float64(whole) + frac
	if neg {
		v = -v
	}
	if percent {
		v /= 100
	}
	return v, true
}
