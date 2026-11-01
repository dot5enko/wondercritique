package render

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildCanvas(t *testing.T) {
	policy, err := newHostPolicy([]string{"images.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	v := Variant{ColorScheme: "dark"}.Normalize()
	fragment := `<div class="root"><style>.root{background:#111}</style><img src="data:image/gif;base64,R0lGOD"></div>`
	doc, err := buildCanvas(policy, v, fragment, true)
	if err != nil {
		t.Fatal(err)
	}

	mustContain := []string{
		"<!doctype html>",
		`<html lang="en" data-wc-canvas="` + CanvasVersion + `"`,
		`<meta charset="utf-8">`,
		`<meta http-equiv="Content-Security-Policy" content="`,
		`default-src 'none'`,
		`script-src 'none'`,
		`style-src 'unsafe-inline' https://fonts.googleapis.com`,
		`font-src https://fonts.gstatic.com data:`,
		`img-src data: https://images.example.com`,
		`media-src data: https://images.example.com`,
		`base-uri 'none'`,
		`form-action 'none'`,
		`<meta name="color-scheme" content="dark">`,
		`color-scheme: dark`,
		`background: #111214`,
		`width: 480px`,
		`min-height: 220px`,
		`<div id="wc-stage">`,
		`<title>wondercritique canvas</title>`,
	}
	for _, want := range mustContain {
		if !strings.Contains(doc, want) {
			t.Errorf("canvas missing %q", want)
		}
	}
	// Fragment injected verbatim, between the stage tags.
	if !strings.Contains(doc, fragment) {
		t.Error("fragment not injected verbatim")
	}
	if strings.Contains(doc, "frame-ancestors") {
		t.Error("frame-ancestors must only appear in the header CSP, not the meta")
	}
	// Fragment comes after the stage opening tag (inside it).
	if strings.Index(doc, `<div id="wc-stage">`) > strings.Index(doc, fragment) {
		t.Error("fragment must be inside the stage")
	}
}

func TestBuildCanvasHeaderCSP(t *testing.T) {
	policy, err := newHostPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	header := policy.csp(true)
	meta := policy.csp(false)
	if !strings.Contains(header, "frame-ancestors 'none'") || !strings.Contains(header, "sandbox") {
		t.Fatalf("header CSP missing directives: %s", header)
	}
	if strings.Contains(meta, "frame-ancestors") || strings.Contains(meta, "sandbox") {
		t.Fatalf("meta CSP must not carry header-only directives: %s", meta)
	}
}

func TestHostPolicyValidation(t *testing.T) {
	ok := []string{"images.example.com", "EXAMPLE.ORG", "a-b.c.example.com"}
	p, err := newHostPolicy(ok)
	if err != nil {
		t.Fatalf("valid hosts rejected: %v", err)
	}
	for _, h := range []string{"Images.Example.COM", "fonts.gstatic.com", "fonts.googleapis.com"} {
		if !p.allowsHost(h) {
			t.Errorf("%s should be allowed", h)
		}
	}
	if p.allowsHost("evil.example.com") {
		t.Error("random host must not be allowed")
	}

	bad := []string{
		"https://example.com", "example.com:8443", "example.com/path", "*.example.com",
		"127.0.0.1", "10.0.0.1", "localhost", "foo.localhost", "bar.local",
		"baz.internal", "qux.invalid", "also.invalid.", "-bad.example.com",
	}
	for _, h := range bad {
		if _, err := newHostPolicy([]string{h}); err == nil {
			t.Errorf("host %q should be rejected", h)
		}
	}
}

func TestHostPolicyAllowsURL(t *testing.T) {
	p, err := newHostPolicy([]string{"cdn.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://fonts.gstatic.com/a.woff2", true},
		{"https://fonts.googleapis.com/css2?family=X", true},
		{"https://cdn.example.com/x.png", true},
		{"https://cdn.example.com:443/x.png", true},
		{"http://cdn.example.com/x.png", false},
		{"https://cdn.example.com:8443/x.png", false},
		{"https://other.example.com/x.png", false},
		{"https://user@cdn.example.com/x.png", false},
		{"data:image/gif;base64,R0lGOD", false}, // data: never goes to the network
	}
	for _, tc := range cases {
		u := parseTestURL(t, tc.url)
		if got := p.allowsURL(u); got != tc.want {
			t.Errorf("%s: allowsURL=%v want %v", tc.url, got, tc.want)
		}
	}
}

func parseTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
