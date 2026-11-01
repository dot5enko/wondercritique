package render

import (
	"math"
	"testing"
)

func TestParseColor(t *testing.T) {
	cases := []struct {
		in   string
		want rgba
		ok   bool
	}{
		{"#fff", rgba{255, 255, 255, 1}, true},
		{"#3366ff", rgba{51, 102, 255, 1}, true},
		{"#111214", rgba{17, 18, 20, 1}, true},
		{"#00000080", rgba{0, 0, 0, 128.0 / 255}, true},
		{"rgb(51, 102, 255)", rgba{51, 102, 255, 1}, true},
		{"rgba(0, 0, 0, 0)", rgba{0, 0, 0, 0}, true},
		{"rgba(10, 20, 30, 0.5)", rgba{10, 20, 30, 0.5}, true},
		{"rgb(51 102 255)", rgba{51, 102, 255, 1}, true},
		{"rgb(51 102 255 / 50%)", rgba{51, 102, 255, 0.5}, true},
		{"rgb(100%, 0%, 0%)", rgba{255, 0, 0, 1}, true},
		{"white", rgba{255, 255, 255, 1}, true},
		{"transparent", rgba{0, 0, 0, 0}, true},
		{"#12345", rgba{}, false},
		{"rgb(1, 2)", rgba{}, false},
		{"not a color", rgba{}, false},
		{"", rgba{}, false},
	}
	for _, tc := range cases {
		got, ok := parseColor(tc.in)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got.r-tc.want.r) > 0.51 || math.Abs(got.g-tc.want.g) > 0.51 ||
			math.Abs(got.b-tc.want.b) > 0.51 || math.Abs(got.a-tc.want.a) > 0.01 {
			t.Errorf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
	}
}

func TestWCAGContrast(t *testing.T) {
	white := rgba{255, 255, 255, 1}
	black := rgba{0, 0, 0, 1}
	if r := round2(wcagContrast(black, white)); r != 21 {
		t.Errorf("black/white = %v, want 21", r)
	}
	if r := round2(wcagContrast(white, white)); r != 1 {
		t.Errorf("white/white = %v, want 1", r)
	}
	// Known WCAG value: #767676 on white is the smallest grey passing AA at 16px.
	grey := rgba{0x76, 0x76, 0x76, 1}
	if r := round2(wcagContrast(grey, white)); r < 4.4 || r > 4.6 {
		t.Errorf("#767676 on white = %v, want ≈4.54", r)
	}
	// #3366ff on white: blue-ish, below 4.5.
	blue := rgba{51, 102, 255, 1}
	if r := wcagContrast(blue, white); r < 2 || r > 6 {
		t.Errorf("blue/white = %v, out of plausible range", r)
	}
}

func TestBlendOver(t *testing.T) {
	// 50% black over white → mid grey.
	got := blendOver(rgba{0, 0, 0, 0.5}, rgba{255, 255, 255, 1})
	if math.Abs(got.r-127.5) > 0.01 {
		t.Errorf("got %+v want r=127.5", got)
	}
	// Opaque over anything → the top color.
	got = blendOver(rgba{10, 20, 30, 1}, rgba{255, 255, 255, 1})
	if got != (rgba{10, 20, 30, 1}) {
		t.Errorf("got %+v", got)
	}
}
