package component

import (
	"sort"
	"strings"
	"testing"
)

func TestCheckLayout(t *testing.T) {
	tests := []struct {
		name  string
		facts LayoutFacts
		want  []string // sorted unique codes; nil = no violations
	}{
		{
			name:  "fits the stage",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 400, ContentHeight: 180, RootWidth: 400, RootHeight: 180, RootLeft: 200, RootTop: 20},
			want:  nil,
		},
		{
			name:  "content taller than stage",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 400, ContentHeight: 340, RootWidth: 400, RootHeight: 340, RootLeft: 200, RootTop: 20},
			want:  []string{CodeStageHeight},
		},
		{
			name:  "default stage height 220",
			facts: LayoutFacts{StageWidth: 800, ContentHeight: 300, RootWidth: 400, RootHeight: 300, RootLeft: 10, RootTop: 10},
			want:  []string{CodeStageHeight},
		},
		{
			name:  "content height falls back to root height",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, RootWidth: 400, RootHeight: 400, RootLeft: 10, RootTop: 10},
			want:  []string{CodeStageHeight},
		},
		{
			name:  "position fixed",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 400, ContentHeight: 100, RootWidth: 400, RootHeight: 100, RootPosition: "fixed"},
			want:  []string{CodeStageWrapper},
		},
		{
			name:  "root covers stage",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 800, ContentHeight: 220, RootWidth: 800, RootHeight: 220, RootLeft: 0, RootTop: 0},
			want:  []string{CodeStageWrapper},
		},
		{
			name:  "root covers stage with opaque background",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 800, ContentHeight: 220, RootWidth: 800, RootHeight: 220, RootBackground: "#ffffff"},
			want:  []string{CodeStageWrapper, CodeOpaqueStage},
		},
		{
			name:  "covers stage with transparent background",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 800, ContentHeight: 220, RootWidth: 800, RootHeight: 220, RootBackground: "rgba(0, 0, 0, 0)"},
			want:  []string{CodeStageWrapper},
		},
		{
			name:  "covers stage with no background reported",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 800, ContentHeight: 220, RootWidth: 800, RootHeight: 220},
			want:  []string{CodeStageWrapper},
		},
		{
			name:  "content wider than stage",
			facts: LayoutFacts{StageWidth: 400, StageHeight: 400, ContentWidth: 600, ContentHeight: 100, RootWidth: 600, RootHeight: 100, RootLeft: 0, RootTop: 0},
			want:  []string{CodeOverflowX},
		},
		{
			name:  "unknown stage width skips overflow and covers",
			facts: LayoutFacts{StageHeight: 220, ContentWidth: 900, ContentHeight: 100, RootWidth: 900, RootHeight: 100, RootLeft: 0, RootTop: 0},
			want:  nil,
		},
		{
			name:  "everything at once",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 900, ContentHeight: 500, RootWidth: 800, RootHeight: 500, RootPosition: "fixed", RootBackground: "rgb(255, 255, 255)"},
			want:  []string{CodeStageHeight, CodeStageWrapper, CodeOpaqueStage, CodeOverflowX},
		},
		{
			name:  "absolute positioning at origin covers stage",
			facts: LayoutFacts{StageWidth: 300, StageHeight: 220, ContentWidth: 300, ContentHeight: 220, RootWidth: 300, RootHeight: 220, RootLeft: 0, RootTop: 0, RootPosition: "absolute"},
			want:  []string{CodeStageWrapper},
		},
		{
			name:  "root slightly outside stage does not cover",
			facts: LayoutFacts{StageWidth: 800, StageHeight: 220, ContentWidth: 700, ContentHeight: 200, RootWidth: 700, RootHeight: 200, RootLeft: 50, RootTop: 10},
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vs := CheckLayout(tc.facts)
			got := codesOf(vs)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("codes = %v, want %v\nviolations:\n%s", got, want, Format(vs))
			}
			for _, v := range vs {
				if v.Line != 0 {
					t.Errorf("layout findings must have Line 0, got %d", v.Line)
				}
				if v.Snippet == "" {
					t.Errorf("%s: want a fact summary snippet", v.Code)
				}
			}
		})
	}
}

func TestOpaqueBackground(t *testing.T) {
	tests := []struct {
		bg   string
		want bool
	}{
		{"", false},
		{"transparent", false},
		{"none", false},
		{"rgba(0, 0, 0, 0)", false},
		{"rgba(0, 0, 0, 0.5)", true},
		{"rgb(255, 255, 255)", true},
		{"#fff", true},
		{"#fff0", false},
		{"#ffff", true},
		{"#ffffffff", true},
		{"#ffffff00", false},
		{"#ffffff80", true},
		{"white", true},
		{"hsla(0, 0%, 0%, 0)", false},
		{"hsla(0, 0%, 0%, 1)", true},
	}
	for _, tc := range tests {
		if got := opaqueBackground(tc.bg); got != tc.want {
			t.Errorf("opaqueBackground(%q) = %v, want %v", tc.bg, got, tc.want)
		}
	}
}
