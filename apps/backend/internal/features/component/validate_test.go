package component

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func readFixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return string(b)
}

// codesOf returns the sorted unique codes of a violation list.
func codesOf(vs []Violation) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if !seen[v.Code] {
			seen[v.Code] = true
			out = append(out, v.Code)
		}
	}
	sort.Strings(out)
	return out
}

var validFixtures = []string{
	"valid/canonical.html",
	"valid/svg-icon.html",
	"valid/media.html",
	"valid/tokens.html",
	"valid/inline-style.html",
	"valid/img-data.html",
}

func TestValidFixtures(t *testing.T) {
	for _, f := range validFixtures {
		t.Run(f, func(t *testing.T) {
			rep := Validate(readFixture(t, f))
			if len(rep.Violations) > 0 {
				t.Errorf("want zero violations, got:\n%s", Format(rep.Violations))
			}
			if !rep.OK() {
				t.Error("want OK() == true")
			}
		})
	}
}

var invalidFixtures = []struct {
	file   string
	want   []string // sorted unique codes
	wantOK bool
}{
	{"invalid/doctype.html", []string{CodeDocumentWrapper}, false},
	{"invalid/document-tags.html", []string{CodeDocumentWrapper}, false},
	{"invalid/script.html", []string{CodeScript}, false},
	{"invalid/script-uppercase.html", []string{CodeScript}, false},
	{"invalid/svg-script.html", []string{CodeScript}, false},
	{"invalid/onload.html", []string{CodeScript}, false},
	{"invalid/js-url.html", []string{CodeScript}, false},
	{"invalid/iframe.html", []string{CodeIframe}, false},
	{"invalid/two-styles.html", []string{CodeStyleBlock}, false},
	{"invalid/unclosed-style.html", []string{CodeStyleBlock}, false},
	{"invalid/font-links-5.html", []string{CodeFontLinks}, false},
	{"invalid/font-links-foreign.html", []string{CodeFontLinks}, false},
	{"invalid/two-roots.html", []string{CodeSingleRoot}, false},
	{"invalid/top-level-text.html", []string{CodeSingleRoot}, false},
	{"invalid/body-selector.html", []string{CodeRootSelectors}, false},
	{"invalid/universal-reset.html", []string{CodeUniversalReset}, false},
	{"invalid/root-color.html", []string{CodeRootTokens}, false},
	{"invalid/unscoped-h3.html", []string{CodeScopedCSS}, false},
	{"invalid/css-import.html", []string{CodeCSSImport}, false},
	{"invalid/external-img.html", []string{CodeExternalResource}, true}, // warning only
	{"invalid/stage-static.html", []string{CodeStageWrapper}, true},     // warning only
}

func TestInvalidFixtures(t *testing.T) {
	for _, tc := range invalidFixtures {
		t.Run(tc.file, func(t *testing.T) {
			rep := Validate(readFixture(t, tc.file))
			got := codesOf(rep.Violations)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("codes = %v, want %v\nviolations:\n%s", got, want, Format(rep.Violations))
			}
			if rep.OK() != tc.wantOK {
				t.Errorf("OK() = %v, want %v\nviolations:\n%s", rep.OK(), tc.wantOK, Format(rep.Violations))
			}
			// every finding must be well-formed and actionable
			for _, v := range rep.Violations {
				if v.Severity != SeverityError && v.Severity != SeverityWarning {
					t.Errorf("bad severity %q for %s", v.Severity, v.Code)
				}
				if v.Line < 0 {
					t.Errorf("negative line %d for %s", v.Line, v.Code)
				}
				if len(v.Message) < 10 {
					t.Errorf("message too short for %s: %q", v.Code, v.Message)
				}
			}
		})
	}
}

func TestTooLarge(t *testing.T) {
	src := strings.Repeat("a", maxFragmentBytes+1)
	rep := Validate(src)
	if got := codesOf(rep.Violations); len(got) != 1 || got[0] != CodeTooLarge {
		t.Fatalf("codes = %v, want [%s]", got, CodeTooLarge)
	}
	if len(rep.Violations) != 1 {
		t.Errorf("want exactly one violation, got %d", len(rep.Violations))
	}
	if rep.OK() {
		t.Error("want OK() == false")
	}
	if rep.Fragment.Bytes != len(src) {
		t.Errorf("Bytes = %d, want %d", rep.Fragment.Bytes, len(src))
	}
}

func TestLineNumbers(t *testing.T) {
	rep := Validate(readFixture(t, "invalid/doctype.html"))
	if len(rep.Violations) != 1 || rep.Violations[0].Line != 1 {
		t.Fatalf("doctype: want 1 violation on line 1, got %+v", rep.Violations)
	}

	rep = Validate(readFixture(t, "invalid/script.html"))
	var lines []int
	for _, v := range rep.Violations {
		if v.Code == CodeScript {
			lines = append(lines, v.Line)
		}
	}
	if len(lines) != 2 || lines[0] != 2 || lines[1] != 3 {
		t.Errorf("script/noscript lines = %v, want [2 3]", lines)
	}
	if rep.Violations[0].Snippet == "" {
		t.Error("want a non-empty snippet")
	}

	rep = Validate(readFixture(t, "invalid/body-selector.html"))
	found := false
	for _, v := range rep.Violations {
		if v.Code == CodeRootSelectors {
			found = true
			if v.Line != 3 {
				t.Errorf("no-root-selectors line = %d, want 3", v.Line)
			}
		}
	}
	if !found {
		t.Error("missing no-root-selectors")
	}
}

func TestFragmentFields(t *testing.T) {
	rep := Validate(readFixture(t, "valid/canonical.html"))
	f := rep.Fragment
	if f.RootTag != "div" {
		t.Errorf("RootTag = %q, want div", f.RootTag)
	}
	if len(f.Links) != 1 || !strings.Contains(f.Links[0], "fonts.googleapis.com") {
		t.Errorf("Links = %v, want one Google Fonts href", f.Links)
	}
	if f.StyleBlocks != 1 {
		t.Errorf("StyleBlocks = %d, want 1", f.StyleBlocks)
	}
	for _, want := range []string{"c1-card", "c1-title", "c1-text"} {
		if !containsString(f.Classes, want) {
			t.Errorf("Classes = %v, missing %s", f.Classes, want)
		}
	}
	if len(f.IDs) != 0 {
		t.Errorf("IDs = %v, want none", f.IDs)
	}
	if f.Bytes != len(readFixture(t, "valid/canonical.html")) {
		t.Errorf("Bytes = %d, want %d", f.Bytes, len(readFixture(t, "valid/canonical.html")))
	}

	// no root when there are two top-level elements
	rep = Validate(readFixture(t, "invalid/two-roots.html"))
	if rep.Fragment.RootTag != "" {
		t.Errorf("RootTag = %q, want empty for two roots", rep.Fragment.RootTag)
	}
}

func TestFormat(t *testing.T) {
	vs := []Violation{
		{Code: CodeScript, Severity: SeverityError, Message: "msg one", Line: 12},
		{Code: CodeExternalResource, Severity: SeverityWarning, Message: "msg two", Line: 0},
	}
	want := "error no-script (line 12): msg one\nwarning external-resource: msg two"
	if got := Format(vs); got != want {
		t.Errorf("Format() = %q, want %q", got, want)
	}
	if got := Format(nil); got != "" {
		t.Errorf("Format(nil) = %q, want empty", got)
	}
}

func TestReportOK(t *testing.T) {
	var nilRep *Report
	if nilRep.OK() {
		t.Error("nil report must not be OK")
	}
	rep := &Report{Violations: []Violation{{Code: CodeExternalResource, Severity: SeverityWarning}}}
	if !rep.OK() {
		t.Error("warnings only → OK")
	}
	rep.Violations = append(rep.Violations, Violation{Code: CodeScript, Severity: SeverityError})
	if rep.OK() {
		t.Error("one error → not OK")
	}
}

// TestRulesCatalog pins the catalog: every finding any fixture produces must
// be declared, with matching severity, and phases must be valid.
func TestRulesCatalog(t *testing.T) {
	catalog := Rules()
	if len(catalog) == 0 {
		t.Fatal("empty catalog")
	}
	byCode := map[string][]Rule{}
	for _, r := range catalog {
		if r.Code == "" || r.Title == "" || r.Description == "" {
			t.Errorf("incomplete rule: %+v", r)
		}
		if r.Phase != PhaseStatic && r.Phase != PhaseLayout {
			t.Errorf("bad phase %q for %s", r.Phase, r.Code)
		}
		if r.Severity != SeverityError && r.Severity != SeverityWarning {
			t.Errorf("bad severity %q for %s", r.Severity, r.Code)
		}
		byCode[r.Code] = append(byCode[r.Code], r)
	}

	files := append([]string(nil), validFixtures...)
	for _, tc := range invalidFixtures {
		files = append(files, tc.file)
	}
	// plus an over-limit input and layout findings
	for _, f := range files {
		rep := Validate(readFixture(t, f))
		for _, v := range rep.Violations {
			rules, ok := byCode[v.Code]
			if !ok {
				t.Errorf("%s: code %q not in Rules()", f, v.Code)
				continue
			}
			sevOK := false
			for _, r := range rules {
				if r.Severity == v.Severity {
					sevOK = true
				}
			}
			if !sevOK {
				t.Errorf("%s: severity %q for %q not in catalog", f, v.Severity, v.Code)
			}
		}
	}
	rep := Validate(strings.Repeat("a", maxFragmentBytes+1))
	for _, v := range rep.Violations {
		if _, ok := byCode[v.Code]; !ok {
			t.Errorf("too-large: code %q not in Rules()", v.Code)
		}
	}
	for _, v := range CheckLayout(LayoutFacts{StageWidth: 100, StageHeight: 10, ContentHeight: 500, ContentWidth: 500}) {
		if _, ok := byCode[v.Code]; !ok {
			t.Errorf("layout: code %q not in Rules()", v.Code)
		}
	}
}

// TestValidateNeverPanics runs a set of adversarial inputs through Validate
// (the fuzz test below covers the rest).
func TestValidateNeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"<",
		"</",
		"<!doctype",
		"<style>",
		"<style></style>",
		"<div",
		"<div class=",
		"<div class='>",
		"<svg><style>",
		"<script>",
		"<template><style></template>",
		"\x00\x01\x02",
		"<div style='position:fixed'>",
		"<style>:root{</style>",
		"<style>@import;</style>",
		"<style>@media{</style>",
		"<style>@media (min-width:1px){</style>",
		"<style>.a{</style>",
		"<style>.a{color:</style>",
		"<style>:is(:is(:is(</style>",
		"<style>*{</style>",
		"<style>#</style>",
		"<style>.</style>",
		"<style>[</style>",
		"<style>a[href^='</style>",
		"<style>@keyframes x{</style>",
		"<style>@font-face{src:url(</style>",
		"<style>@layer{</style>",
		"<style>--x:1</style>",
		"<!---->",
		"<?xml version='1.0'?>",
		"<![CDATA[x]]>",
		"<br>",
		"</br>",
		"</div>",
		"<div/></div>",
		"<math><mtext><script>x</script></mtext></math>",
		"<noscript><p>x</p></noscript>",
		strings.Repeat("<div>", 500),
		strings.Repeat("@media{", 200),
		strings.Repeat(":is(", 200),
	}
	for i, src := range inputs {
		rep := Validate(src)
		if rep == nil {
			t.Fatalf("input %d: nil report", i)
		}
		_ = rep.OK()
		_ = Format(rep.Violations)
	}
}
