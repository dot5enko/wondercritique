// Package render turns an untrusted HTML component fragment into screenshots and layout/style metrics.
//
// The fragment is wrapped in a renderer-owned canvas document (see canvas.go), loaded in a sandboxed
// headless Chrome with page JavaScript disabled and the network locked down (see chrome.go, filter.go
// and proxy.go), and captured once per Variant. See docs/backend.render.md.
package render

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Renderer renders HTML component fragments. Implementations are safe for concurrent use.
type Renderer interface {
	Render(ctx context.Context, req Request) (*Result, error)
}

// Request is one render job: a fragment captured in one or more variants.
type Request struct {
	// HTML is the component fragment: optional Google Fonts <link>s, an optional <style> and one component
	// element. It is injected verbatim into the canvas stage; the renderer does not validate its structure.
	HTML string `json:"html"`
	// Variants to capture, in order. Empty means one DefaultVariant.
	Variants []Variant `json:"variants,omitempty"`
}

// Variant describes one capture of the fragment. Zero fields get defaults (see Normalize).
type Variant struct {
	Name              string  `json:"name"`                // default "light" / "dark" after ColorScheme
	Width             int     `json:"width"`               // stage and viewport width in CSS px, default 480
	StageHeight       int     `json:"stage_height"`        // stage min-height in CSS px, default 220
	Background        string  `json:"background"`          // canvas CSS color, default #ffffff (light) / #111214 (dark); "transparent" gives an alpha PNG
	ColorScheme       string  `json:"color_scheme"`        // "light" | "dark": emulated prefers-color-scheme and canvas color-scheme
	DeviceScaleFactor float64 `json:"device_scale_factor"` // default 2
	MaxHeight         int     `json:"max_height"`          // capture height cap in CSS px, default 1200
}

// Result is the outcome of a render.
type Result struct {
	CanvasVersion string `json:"canvas_version"`
	Shots         []Shot `json:"shots"`
}

// Shot is one captured variant.
type Shot struct {
	Variant Variant `json:"variant"` // normalized
	PNG     []byte  `json:"-"`
	Width   int     `json:"width"`  // captured CSS px (the PNG is Width*DeviceScaleFactor wide)
	Height  int     `json:"height"` // captured CSS px
	Metrics Metrics `json:"metrics"`
}

// Defaults and limits.
const (
	DefaultWidth             = 480
	DefaultStageHeight       = 220
	DefaultMaxHeight         = 1200
	DefaultDeviceScaleFactor = 2.0
	DefaultLightBackground   = "#ffffff"
	DefaultDarkBackground    = "#111214"

	MaxHTMLBytes = 256 << 10
	MaxVariants  = 8
	minWidth     = 120
	maxWidth     = 2400
	minStage     = 40
	maxStage     = 2400
	maxCapture   = 4800
)

var (
	// ErrInvalidRequest is wrapped by every request validation error.
	ErrInvalidRequest = errors.New("render: invalid request")
	// ErrNoBrowser means no Chrome/Chromium executable was found or configured.
	ErrNoBrowser = errors.New("render: no Chrome/Chromium executable found (set RENDER_CHROME_PATH)")
	// ErrClosed is returned after the renderer was closed.
	ErrClosed = errors.New("render: renderer closed")
)

// DefaultVariant is the variant used when a request has none.
func DefaultVariant() Variant { return Variant{}.Normalize() }

// LightDark returns the standard light + dark pair at the default size.
func LightDark() []Variant {
	return []Variant{Variant{ColorScheme: "light"}.Normalize(), Variant{ColorScheme: "dark"}.Normalize()}
}

// Normalize fills defaults. It does not validate; see Validate.
func (v Variant) Normalize() Variant {
	v.ColorScheme = strings.ToLower(strings.TrimSpace(v.ColorScheme))
	if v.ColorScheme == "" {
		v.ColorScheme = "light"
	}
	if v.Name == "" {
		v.Name = v.ColorScheme
	}
	if v.Width == 0 {
		v.Width = DefaultWidth
	}
	if v.StageHeight == 0 {
		v.StageHeight = DefaultStageHeight
	}
	v.Background = strings.TrimSpace(v.Background)
	if v.Background == "" {
		v.Background = DefaultLightBackground
		if v.ColorScheme == "dark" {
			v.Background = DefaultDarkBackground
		}
	}
	if v.DeviceScaleFactor == 0 {
		v.DeviceScaleFactor = DefaultDeviceScaleFactor
	}
	if v.MaxHeight == 0 {
		v.MaxHeight = max(DefaultMaxHeight, v.StageHeight)
	}
	return v
}

// cssColorRe accepts hex, named and functional colors without any character that could escape a CSS
// declaration (no ; { } < > quotes or backslashes).
var cssColorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{3,30}|(rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch|color)\([0-9a-zA-Z.,%/ +-]{1,100}\))$`)

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,40}$`)

// Validate checks a normalized variant.
func (v Variant) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: variant %q: %s", ErrInvalidRequest, v.Name, fmt.Sprintf(format, a...))
	}
	switch {
	case !nameRe.MatchString(v.Name):
		return bad("name must match %s", nameRe)
	case v.ColorScheme != "light" && v.ColorScheme != "dark":
		return bad("color_scheme must be light or dark")
	case v.Width < minWidth || v.Width > maxWidth:
		return bad("width must be in [%d, %d]", minWidth, maxWidth)
	case v.StageHeight < minStage || v.StageHeight > maxStage:
		return bad("stage_height must be in [%d, %d]", minStage, maxStage)
	case v.MaxHeight < v.StageHeight || v.MaxHeight > maxCapture:
		return bad("max_height must be in [stage_height, %d]", maxCapture)
	case v.DeviceScaleFactor < 1 || v.DeviceScaleFactor > 3:
		return bad("device_scale_factor must be in [1, 3]")
	case !cssColorRe.MatchString(v.Background):
		return bad("background is not an accepted CSS color")
	}
	return nil
}

// normalize returns the normalized variants of the request or a validation error.
func (r Request) normalize() ([]Variant, error) {
	if strings.TrimSpace(r.HTML) == "" {
		return nil, fmt.Errorf("%w: empty html", ErrInvalidRequest)
	}
	if len(r.HTML) > MaxHTMLBytes {
		return nil, fmt.Errorf("%w: html is %d bytes, limit %d", ErrInvalidRequest, len(r.HTML), MaxHTMLBytes)
	}
	if len(r.Variants) > MaxVariants {
		return nil, fmt.Errorf("%w: %d variants, limit %d", ErrInvalidRequest, len(r.Variants), MaxVariants)
	}
	vs := r.Variants
	if len(vs) == 0 {
		vs = []Variant{{}}
	}
	out := make([]Variant, len(vs))
	for i, v := range vs {
		out[i] = v.Normalize()
		if err := out[i].Validate(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
