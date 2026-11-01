package render

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/domsnapshot"
)

// Metrics describes the layout and styles of one captured variant, measured
// from the controller side with CDP (page JavaScript is disabled).
type Metrics struct {
	// Stage is the capture stage box in viewport coordinates (x is always 0).
	Stage Box `json:"stage"`
	// Root is the component root element box relative to the stage.
	Root Box `json:"root"`
	// StageHeight is the stage box height in CSS px (>= variant StageHeight).
	StageHeight int `json:"stage_height"`
	// ContentHeight/ContentWidth are the extents of everything inside the
	// stage, relative to the stage top-left corner.
	ContentHeight int `json:"content_height"`
	ContentWidth  int `json:"content_width"`
	// OverflowX/OverflowY report content escaping the stage box.
	OverflowX bool `json:"overflow_x"`
	OverflowY bool `json:"overflow_y"`
	// TopLevelElementCount is the number of element children of the stage.
	TopLevelElementCount int `json:"top_level_elements"`
	// BlockedRequests are requests denied by CSP or the request filter.
	BlockedRequests []BlockedRequest `json:"blocked_requests,omitempty"`
	// FailedRequests are requests that reached the network but failed
	// (proxy refusal, DNS, connection errors).
	FailedRequests []FailedRequest `json:"failed_requests,omitempty"`
	// TextRuns are the post-layout text runs with resolved styles and WCAG
	// contrast (0 = not computable).
	TextRuns []TextRun `json:"text_runs,omitempty"`
}

// Box is a rectangle in CSS px.
type Box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// BlockedRequest is a request denied before it left the browser.
type BlockedRequest struct {
	URL    string `json:"url"`
	Reason string `json:"reason"` // "content-security-policy", "not-allowlisted", "scheme-not-allowed", "port-not-allowed", "invalid-url"
}

// FailedRequest is a request that failed in the network stack.
type FailedRequest struct {
	URL   string `json:"url"`
	Error string `json:"error"` // e.g. net::ERR_PROXY_CONNECTION_FAILED
}

// TextRun is one post-layout text run with the styles a critic needs.
type TextRun struct {
	Text       string  `json:"text"`
	Color      string  `json:"color"`       // computed color of the run
	Background string  `json:"background"`  // resolved background the run sits on
	FontSize   string  `json:"font_size"`   // computed, e.g. "16px"
	FontWeight string  `json:"font_weight"` // computed, e.g. "700"
	FontFamily string  `json:"font_family"` // computed
	Contrast   float64 `json:"contrast"`    // WCAG 2.x contrast ratio; 0 = not computable
}

// measuredStyles is the DOMSnapshot computed-style whitelist. The order here
// defines the position of each value in Layout.Styles.
var measuredStyles = []string{"color", "background-color", "font-size", "font-weight", "font-family"}

const (
	styleColor = iota
	styleBackground
	styleFontSize
	styleFontWeight
	styleFontFamily
)

const (
	maxTextRuns   = 150
	maxRunTextLen = 240
	stageID       = "wc-stage"
)

// computeMetrics turns a DOMSnapshot into Metrics and returns the capture
// height (stage area, grown to content, capped at the variant MaxHeight).
func computeMetrics(res *domsnapshot.CaptureSnapshotResult, v Variant, f *requestFilter) (*Metrics, int, error) {
	if len(res.Documents) == 0 {
		return nil, 0, errors.New("snapshot has no document")
	}
	d := res.Documents[0]
	if d.Nodes == nil || d.Layout == nil {
		return nil, 0, errors.New("snapshot has no node/layout tables")
	}
	nodeCount := len(d.Nodes.NodeName)

	stageIdx := -1
	for i := 0; i < nodeCount; i++ {
		if nodeType(d, i) == 1 && nodeAttr(d, res, i, "id") == stageID {
			stageIdx = i
			break
		}
	}
	if stageIdx < 0 {
		return nil, 0, errors.New("stage element not found")
	}
	domToLayout := map[int64]int64{}
	for li, di := range d.Layout.NodeIndex {
		if _, seen := domToLayout[di]; !seen {
			domToLayout[di] = int64(li)
		}
	}
	stageLi, ok := domToLayout[int64(stageIdx)]
	if !ok || int(stageLi) >= len(d.Layout.Bounds) {
		return nil, 0, errors.New("stage has no layout box")
	}
	stageB := rectOf(d.Layout.Bounds[stageLi])

	// Element children of the stage: the component root is the first one.
	children := childIndexes(d, stageIdx)
	rootIdx := int64(-1)
	topLevel := 0
	for _, ci := range children {
		if nodeType(d, int(ci)) == 1 {
			topLevel++
			if rootIdx < 0 {
				rootIdx = ci
			}
		}
	}

	// Content union over the stage's DOM subtree (excluding the stage itself).
	subtree := subtreeOf(d, stageIdx)
	union := rect{}
	hasUnion := false
	for li, domIdx := range d.Layout.NodeIndex {
		if domIdx == int64(stageIdx) || !subtree[domIdx] || li >= len(d.Layout.Bounds) {
			continue
		}
		if r := rectOf(d.Layout.Bounds[li]); r.w > 0 || r.h > 0 || r.x != 0 || r.y != 0 {
			union.add(r)
			hasUnion = true
		}
	}

	m := &Metrics{
		Stage: Box{
			X:      round(stageB.x),
			Y:      round(stageB.y),
			Width:  round(stageB.w),
			Height: round(stageB.h),
		},
		StageHeight:          round(stageB.h),
		TopLevelElementCount: topLevel,
	}
	if rootIdx >= 0 {
		if rli, ok := domToLayout[rootIdx]; ok && int(rli) < len(d.Layout.Bounds) {
			rb := rectOf(d.Layout.Bounds[rli])
			m.Root = Box{
				X:      round(rb.x - stageB.x),
				Y:      round(rb.y - stageB.y),
				Width:  round(rb.w),
				Height: round(rb.h),
			}
		}
	}

	contentHeight := 0.0
	if hasUnion {
		m.ContentWidth = round(union.w)
		contentHeight = math.Max(0, union.y+union.h-stageB.y)
		m.ContentHeight = round(contentHeight)
		m.OverflowX = union.x < stageB.x-0.5 || union.x+union.w > stageB.x+stageB.w+0.5
		m.OverflowY = union.y < stageB.y-0.5 || union.y+union.h > stageB.y+stageB.h+0.5
	}

	m.BlockedRequests, m.FailedRequests = f.snapshot()
	m.TextRuns = collectTextRuns(res, d, domToLayout, v)

	capture := math.Max(stageB.h, contentHeight)
	capture = math.Min(capture, float64(v.MaxHeight))
	captureHeight := int(math.Ceil(capture))
	if captureHeight < 1 {
		captureHeight = 1
	}
	return m, captureHeight, nil
}

// collectTextRuns extracts the post-layout text runs with their resolved
// styles and WCAG contrast.
func collectTextRuns(res *domsnapshot.CaptureSnapshotResult, d *domsnapshot.DocumentSnapshot, domToLayout map[int64]int64, v Variant) []TextRun {
	var runs []TextRun
	for li := range d.Layout.NodeIndex {
		if li >= len(d.Layout.Text) {
			break
		}
		ti := d.Layout.Text[li]
		if ti == 0 {
			continue
		}
		text := strings.TrimSpace(str(res, ti))
		if text == "" {
			continue
		}
		if len(runs) >= maxTextRuns {
			break
		}
		styles := decodeStyles(res, d, li)
		color := styles[styleColor]
		bg := resolveBackground(res, d, domToLayout, int(d.Layout.NodeIndex[li]), v)
		textRun := TextRun{
			Text:       truncateRun(text),
			Color:      color,
			Background: bg,
			FontSize:   styles[styleFontSize],
			FontWeight: styles[styleFontWeight],
			FontFamily: styles[styleFontFamily],
		}
		if fg, ok := parseColor(color); ok {
			if base, ok := parseColor(bg); ok {
				fg = blendOver(fg, base)
				textRun.Contrast = round2(wcagContrast(fg, base))
			}
		}
		runs = append(runs, textRun)
	}
	return runs
}

// resolveBackground walks up from node collecting ancestor backgrounds and
// folds them over the canvas background (white when the variant background is
// transparent). It returns the effective background as rgb().
func resolveBackground(res *domsnapshot.CaptureSnapshotResult, d *domsnapshot.DocumentSnapshot, domToLayout map[int64]int64, node int, v Variant) string {
	var layers []string
	cur := node
	for steps := 0; cur >= 0 && steps < 10000; steps++ {
		if nodeType(d, cur) == 9 { // #document
			break
		}
		if bg := elementBackground(res, d, domToLayout, cur); bg != "" {
			layers = append(layers, bg)
		}
		parent := parentOf(d, cur)
		if parent < 0 || parent == cur {
			break
		}
		cur = parent
	}

	effective := rgba{r: 255, g: 255, b: 255, a: 1} // white backdrop
	if bg, ok := parseColor(v.Background); ok {
		effective = blendOver(bg, effective)
	}
	for i := len(layers) - 1; i >= 0; i-- {
		if bg, ok := parseColor(layers[i]); ok && bg.a > 0 {
			effective = blendOver(bg, effective)
		}
	}
	return fmt.Sprintf("rgb(%d, %d, %d)", round(effective.r), round(effective.g), round(effective.b))
}

// elementBackground returns the node's own computed background-color, or ""
// when the node has no layout box (it cannot paint behind text).
func elementBackground(res *domsnapshot.CaptureSnapshotResult, d *domsnapshot.DocumentSnapshot, domToLayout map[int64]int64, node int) string {
	li, ok := domToLayout[int64(node)]
	if !ok || int(li) >= len(d.Layout.Styles) {
		return ""
	}
	return decodeStyles(res, d, int(li))[styleBackground]
}

func decodeStyles(res *domsnapshot.CaptureSnapshotResult, d *domsnapshot.DocumentSnapshot, layoutIdx int) []string {
	out := make([]string, len(measuredStyles))
	if layoutIdx < 0 || layoutIdx >= len(d.Layout.Styles) {
		return out
	}
	for i, s := range d.Layout.Styles[layoutIdx] {
		if i >= len(out) {
			break
		}
		out[i] = str(res, s)
	}
	return out
}

func str[T ~int64](res *domsnapshot.CaptureSnapshotResult, i T) string {
	if i < 0 || int(i) >= len(res.Strings) {
		return ""
	}
	return res.Strings[i]
}

func nodeType(d *domsnapshot.DocumentSnapshot, i int) int64 {
	if i < 0 || i >= len(d.Nodes.NodeType) {
		return -1
	}
	return d.Nodes.NodeType[i]
}

// nodeAttr returns an element attribute value ("" when absent).
func nodeAttr(d *domsnapshot.DocumentSnapshot, res *domsnapshot.CaptureSnapshotResult, i int, name string) string {
	if i < 0 || i >= len(d.Nodes.Attributes) {
		return ""
	}
	attrs := d.Nodes.Attributes[i]
	for j := 0; j+1 < len(attrs); j += 2 {
		if str(res, attrs[j]) == name {
			return str(res, attrs[j+1])
		}
	}
	return ""
}

func parentOf(d *domsnapshot.DocumentSnapshot, i int) int {
	if i < 0 || i >= len(d.Nodes.ParentIndex) {
		return -1
	}
	p := d.Nodes.ParentIndex[i]
	if p < 0 || int(p) == i || int(p) >= len(d.Nodes.NodeName) {
		return -1
	}
	return int(p)
}

// childIndexes lists the DOM children of a node (ParentIndex is the only
// structural table DOMSnapshot gives us; children are recovered by inversion).
func childIndexes(d *domsnapshot.DocumentSnapshot, node int) []int64 {
	var out []int64
	for i := range d.Nodes.NodeName {
		if parentOf(d, i) == node {
			out = append(out, int64(i))
		}
	}
	return out
}

// subtreeOf collects a node and all of its DOM descendants.
func subtreeOf(d *domsnapshot.DocumentSnapshot, node int) map[int64]bool {
	out := map[int64]bool{int64(node): true}
	stack := []int{node}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range childIndexes(d, n) {
			if !out[c] {
				out[c] = true
				stack = append(stack, int(c))
			}
		}
	}
	return out
}

type rect struct{ x, y, w, h float64 }

func rectOf(b []float64) rect {
	if len(b) < 4 {
		return rect{}
	}
	return rect{x: b[0], y: b[1], w: b[2], h: b[3]}
}

func (r *rect) add(o rect) {
	if !r.valid() {
		*r = o
		return
	}
	x2 := math.Max(r.x+r.w, o.x+o.w)
	y2 := math.Max(r.y+r.h, o.y+o.h)
	r.x = math.Min(r.x, o.x)
	r.y = math.Min(r.y, o.y)
	r.w = x2 - r.x
	r.h = y2 - r.y
}

func (r rect) valid() bool { return r.w != 0 || r.h != 0 || r.x != 0 || r.y != 0 }

func round(f float64) int { return int(math.Round(f)) }

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func truncateRun(s string) string {
	runes := []rune(s)
	if len(runes) <= maxRunTextLen {
		return s
	}
	return string(runes[:maxRunTextLen]) + "…"
}

// --- color math -------------------------------------------------------------

// rgba is a color with channels 0–255 and alpha 0–1.
type rgba struct{ r, g, b, a float64 }

// parseColor parses the serializations Chrome emits for computed colors
// (#hex, rgb()/rgba() in comma or space syntax, percentages) plus the handful
// of named colors a variant background may use. Unknown serializations return
// ok=false (callers then report contrast 0).
func parseColor(s string) (rgba, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return rgba{}, false
	}
	if strings.HasPrefix(s, "#") {
		return parseHex(s[1:])
	}
	if name, ok := namedColors[s]; ok {
		return name, true
	}
	if strings.HasPrefix(s, "rgb(") || strings.HasPrefix(s, "rgba(") {
		inner := s[strings.Index(s, "(")+1 : len(s)-1]
		inner = strings.ReplaceAll(inner, ",", " ")
		inner = strings.ReplaceAll(inner, "/", " ")
		fields := strings.Fields(inner)
		if len(fields) < 3 {
			return rgba{}, false
		}
		var ch [3]float64
		for i := 0; i < 3; i++ {
			v, err := cssNumber(fields[i])
			if err != nil {
				return rgba{}, false
			}
			if strings.HasSuffix(fields[i], "%") {
				ch[i] = v * 255 / 100
			} else {
				ch[i] = v
			}
		}
		a := 1.0
		if len(fields) >= 4 {
			v, err := cssNumber(fields[3])
			if err != nil {
				return rgba{}, false
			}
			if strings.HasSuffix(fields[3], "%") {
				a = v / 100
			} else {
				a = v
			}
		}
		return rgba{clamp255(ch[0]), clamp255(ch[1]), clamp255(ch[2]), clamp01(a)}, true
	}
	return rgba{}, false
}

func cssNumber(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
}

func parseHex(h string) (rgba, bool) {
	switch len(h) {
	case 3, 4:
		var v [4]int
		for i := 0; i < len(h); i++ {
			d, ok := hexVal(h[i])
			if !ok {
				return rgba{}, false
			}
			v[i] = d * 17
		}
		if len(h) == 3 {
			return rgba{float64(v[0]), float64(v[1]), float64(v[2]), 1}, true
		}
		return rgba{float64(v[0]), float64(v[1]), float64(v[2]), float64(v[3]) / 255}, true
	case 6, 8:
		var v [4]int
		for i := 0; i < len(h); i += 2 {
			hi, ok1 := hexVal(h[i])
			lo, ok2 := hexVal(h[i+1])
			if !ok1 || !ok2 {
				return rgba{}, false
			}
			v[i/2] = hi*16 + lo
		}
		if len(h) == 6 {
			return rgba{float64(v[0]), float64(v[1]), float64(v[2]), 1}, true
		}
		return rgba{float64(v[0]), float64(v[1]), float64(v[2]), float64(v[3]) / 255}, true
	}
	return rgba{}, false
}

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func clamp255(f float64) float64 { return math.Max(0, math.Min(255, f)) }
func clamp01(f float64) float64  { return math.Max(0, math.Min(1, f)) }

// blendOver composites fg over bg (both non-premultiplied).
func blendOver(fg, bg rgba) rgba {
	a := fg.a
	return rgba{
		r: fg.r*a + bg.r*(1-a),
		g: fg.g*a + bg.g*(1-a),
		b: fg.b*a + bg.b*(1-a),
		a: a + bg.a*(1-a),
	}
}

// wcagContrast is the WCAG 2.x contrast ratio of two opaque colors: (L1+0.05)/(L2+0.05).
func wcagContrast(fg, bg rgba) float64 {
	l1 := relativeLuminance(fg)
	l2 := relativeLuminance(bg)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func relativeLuminance(c rgba) float64 {
	lin := func(v float64) float64 {
		v /= 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.r) + 0.7152*lin(c.g) + 0.0722*lin(c.b)
}

// namedColors covers the names a variant background may legally contain
// (cssColorRe); computed styles from Chrome are rgb()/hex, which the parser
// handles above.
var namedColors = map[string]rgba{
	"transparent": {0, 0, 0, 0},
	"white":       {255, 255, 255, 1},
	"black":       {0, 0, 0, 1},
	"red":         {255, 0, 0, 1},
	"green":       {0, 128, 0, 1},
	"blue":        {0, 0, 255, 1},
	"gray":        {128, 128, 128, 1},
	"grey":        {128, 128, 128, 1},
	"silver":      {192, 192, 192, 1},
	"orange":      {255, 165, 0, 1},
	"yellow":      {255, 255, 0, 1},
	"purple":      {128, 0, 128, 1},
	"pink":        {255, 192, 203, 1},
	"brown":       {165, 42, 42, 1},
	"cyan":        {0, 255, 255, 1},
	"magenta":     {255, 0, 255, 1},
	"lime":        {0, 255, 0, 1},
	"navy":        {0, 0, 128, 1},
	"teal":        {0, 128, 128, 1},
	"olive":       {128, 128, 0, 1},
	"maroon":      {128, 0, 0, 1},
	"indigo":      {75, 0, 130, 1},
	"violet":      {238, 130, 238, 1},
	"gold":        {255, 215, 0, 1},
	"crimson":     {220, 20, 60, 1},
	"salmon":      {250, 128, 114, 1},
	"khaki":       {240, 230, 140, 1},
	"azure":       {240, 255, 255, 1},
	"beige":       {245, 245, 220, 1},
	"ivory":       {255, 255, 240, 1},
	"snow":        {255, 250, 250, 1},
}
