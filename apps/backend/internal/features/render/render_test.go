package render

import (
	"errors"
	"strings"
	"testing"
)

func TestVariantNormalizeDefaults(t *testing.T) {
	v := DefaultVariant()
	if v.Name != "light" || v.ColorScheme != "light" {
		t.Fatalf("defaults: %+v", v)
	}
	if v.Width != DefaultWidth || v.StageHeight != DefaultStageHeight ||
		v.DeviceScaleFactor != DefaultDeviceScaleFactor || v.MaxHeight != DefaultMaxHeight {
		t.Fatalf("size defaults: %+v", v)
	}
	if v.Background != DefaultLightBackground {
		t.Fatalf("light background: %q", v.Background)
	}
}

func TestLightDark(t *testing.T) {
	vs := LightDark()
	if len(vs) != 2 {
		t.Fatalf("want 2 variants, got %d", len(vs))
	}
	if vs[0].Name != "light" || vs[1].Name != "dark" {
		t.Fatalf("names: %q %q", vs[0].Name, vs[1].Name)
	}
	if vs[1].Background != DefaultDarkBackground {
		t.Fatalf("dark background: %q", vs[1].Background)
	}
	for _, v := range vs {
		if err := v.Validate(); err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
	}
}

func TestVariantNormalizeExplicit(t *testing.T) {
	v := Variant{Name: "wide", Width: 800, StageHeight: 300, Background: "#123456",
		ColorScheme: " DARK ", DeviceScaleFactor: 1.5, MaxHeight: 600}.Normalize()
	if v.ColorScheme != "dark" {
		t.Fatalf("color scheme not lowercased: %q", v.ColorScheme)
	}
	// Name is only defaulted from ColorScheme when empty; explicit wins.
	if v.Name != "wide" {
		t.Fatalf("name: %q", v.Name)
	}
	if v.Width != 800 || v.StageHeight != 300 || v.DeviceScaleFactor != 1.5 || v.MaxHeight != 600 {
		t.Fatalf("explicit values changed: %+v", v)
	}
	if v.Background != "#123456" {
		t.Fatalf("background: %q", v.Background)
	}
}

func TestRequestValidation(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"empty html", Request{}, "empty html"},
		{"too many variants", Request{HTML: "<div>x</div>", Variants: make([]Variant, MaxVariants+1)}, "variants, limit"},
		{"bad name", Request{HTML: "<div>x</div>", Variants: []Variant{{Name: "bad name!"}}}, "name must match"},
		{"bad scheme", Request{HTML: "<div>x</div>", Variants: []Variant{{ColorScheme: "sepia"}}}, "color_scheme"},
		{"width too small", Request{HTML: "<div>x</div>", Variants: []Variant{{Width: 10}}}, "width"},
		{"stage too small", Request{HTML: "<div>x</div>", Variants: []Variant{{StageHeight: 5}}}, "stage_height"},
		{"max below stage", Request{HTML: "<div>x</div>", Variants: []Variant{{StageHeight: 500, MaxHeight: 300}}}, "max_height"},
		{"dsr too high", Request{HTML: "<div>x</div>", Variants: []Variant{{DeviceScaleFactor: 4}}}, "device_scale_factor"},
		{"bad background", Request{HTML: "<div>x</div>", Variants: []Variant{{Background: `red; } body {`}}}, "background"},
		{"ok", Request{HTML: "<div>x</div>", Variants: []Variant{{}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.req.normalize()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestRequestHTMLTooLarge(t *testing.T) {
	req := Request{HTML: strings.Repeat("a", MaxHTMLBytes+1)}
	_, err := req.normalize()
	if err == nil || !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("want size limit error, got %v", err)
	}
}

func TestNormalizeNoVariantsYieldsOne(t *testing.T) {
	vs, err := Request{HTML: "<div>x</div>"}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Name != "light" {
		t.Fatalf("variants: %+v", vs)
	}
}
