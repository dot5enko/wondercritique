package component

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidate asserts that Validate never panics, always returns a report,
// and that every finding is well-formed, for arbitrary input.
//
// Run with: go test -fuzz=FuzzValidate -fuzztime=20s
func FuzzValidate(f *testing.F) {
	// seeds: every fixture plus a few hand-picked adversarial snippets
	_ = filepath.Walk("testdata", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil {
			f.Add(string(b))
		}
		return nil
	})
	seeds := []string{
		"",
		"<div>",
		"<style>.a{color:red}</style>",
		"<style>@media{",
		"<style>:is(:has(",
		"<script>x</script>",
		"<!doctype html>",
		"<svg><style>.a{}</style></svg>",
		"*{box-sizing:border-box}",
		":root{color:red}",
		"<a href='javascript:1'>",
		strings.Repeat("@container{", 50),
		strings.Repeat("<div>", 200),
		"\xff\xfe\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		rep := Validate(src)
		if rep == nil {
			t.Fatal("Validate returned nil")
		}
		if rep.Fragment.Bytes != len(src) && len(src) <= maxFragmentBytes {
			t.Errorf("Bytes = %d, want %d", rep.Fragment.Bytes, len(src))
		}
		_ = rep.OK()
		_ = Format(rep.Violations)
		seen := map[string]bool{}
		for _, v := range rep.Violations {
			if v.Code == "" {
				t.Error("empty violation code")
			}
			if v.Severity != SeverityError && v.Severity != SeverityWarning {
				t.Errorf("bad severity %q", v.Severity)
			}
			if v.Line < 0 {
				t.Errorf("negative line %d", v.Line)
			}
			if seen[v.Code+string(v.Severity)] && v.Severity == SeverityError && v.Code == CodeInternal {
				t.Error("multiple internal-error findings")
			}
			seen[v.Code+string(v.Severity)] = true
		}
		// layout must be panic-free for arbitrary measurements too
		_ = CheckLayout(LayoutFacts{
			StageWidth:     float64(len(src) % 1000),
			StageHeight:    float64(len(src) % 300),
			ContentWidth:   float64(len(src) % 2000),
			ContentHeight:  float64(len(src) % 600),
			RootLeft:       float64(-(len(src) % 50)),
			RootTop:        float64(len(src) % 50),
			RootWidth:      float64(len(src) % 1000),
			RootHeight:     float64(len(src) % 400),
			RootPosition:   src,
			RootBackground: src,
		})
	})
}
