package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"text/template"
)

// CanvasVersion identifies the canvas document (template, reset, CSP, stage layout). Bump it whenever
// canvas.html or the CSP changes, because screenshots of the same fragment are only comparable within
// one canvas version.
const CanvasVersion = "wc-canvas/1"

// canvasOrigin is the fake origin the canvas is served from. The Fetch interceptor fulfills the
// document itself; nothing on this host is ever fetched from the network (.invalid never resolves and
// the proxy refuses it).
const (
	canvasHost   = "canvas.wondercritique.invalid"
	canvasOrigin = "https://" + canvasHost
)

// Hosts the fragment may always load from: Google Fonts CSS and font files.
const (
	fontCSSHost  = "fonts.googleapis.com"
	fontFileHost = "fonts.gstatic.com"
)

//go:embed canvas.html
var canvasHTML string

var canvasTmpl = template.Must(template.New("canvas").Parse(canvasHTML))

// hostPolicy is the network allowlist shared by the CSP, the Fetch filter and the proxy.
type hostPolicy struct {
	extra []string        // extra image/media hosts, validated, lowercase
	all   map[string]bool // fonts hosts + extra
}

var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// newHostPolicy validates the extra hosts: plain DNS names only (no scheme, port, path, wildcard or IP
// literal), because they are also spliced into the CSP.
func newHostPolicy(extra []string) (*hostPolicy, error) {
	p := &hostPolicy{all: map[string]bool{fontCSSHost: true, fontFileHost: true}}
	for _, h := range extra {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" {
			continue
		}
		if !hostnameRe.MatchString(h) || net.ParseIP(h) != nil || strings.HasSuffix(h, ".invalid") ||
			strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
			return nil, fmt.Errorf("render: allowed host %q must be a public DNS name without scheme, port or wildcard", h)
		}
		if !p.all[h] {
			p.all[h] = true
			p.extra = append(p.extra, h)
		}
	}
	return p, nil
}

// allowsHost reports whether host (lowercase, no port) is on the allowlist.
func (p *hostPolicy) allowsHost(host string) bool {
	return p.all[strings.TrimSuffix(strings.ToLower(host), ".")]
}

// allowsURL reports whether the page may fetch u from the network: https on the default port to an
// allowlisted host, no credentials.
func (p *hostPolicy) allowsURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	return p.allowsHost(u.Hostname())
}

// csp builds the canvas Content-Security-Policy. header adds the directives that are only valid in a
// response header (frame-ancestors, sandbox); the <meta> copy omits them.
func (p *hostPolicy) csp(header bool) string {
	media := "data:"
	for _, h := range p.extra {
		media += " https://" + h
	}
	d := []string{
		"default-src 'none'",
		"script-src 'none'",
		"style-src 'unsafe-inline' https://" + fontCSSHost,
		"font-src https://" + fontFileHost + " data:",
		"img-src " + media,
		"media-src " + media,
		"base-uri 'none'",
		"form-action 'none'",
	}
	if header {
		d = append(d, "frame-ancestors 'none'", "sandbox")
	}
	return strings.Join(d, "; ")
}

type canvasData struct {
	Version     string
	CSP         string
	ColorScheme string
	Background  string
	Width       int
	StageHeight int
	Fragment    string
}

// buildCanvas renders the canvas document for one variant with the fragment injected verbatim.
// Every templated value except the fragment is either a constant, an int or validated (Background by
// cssColorRe, ColorScheme by Validate, hosts by newHostPolicy).
func buildCanvas(p *hostPolicy, v Variant, fragment string, withCSP bool) (string, error) {
	csp := ""
	if withCSP {
		csp = p.csp(false)
	}
	var b bytes.Buffer
	b.Grow(len(canvasHTML) + len(fragment) + 256)
	err := canvasTmpl.Execute(&b, canvasData{
		Version:     CanvasVersion,
		CSP:         csp,
		ColorScheme: v.ColorScheme,
		Background:  v.Background,
		Width:       v.Width,
		StageHeight: v.StageHeight,
		Fragment:    fragment,
	})
	if err != nil {
		return "", fmt.Errorf("render: build canvas: %w", err)
	}
	return b.String(), nil
}

// canvasURL is the document URL of variant i.
func canvasURL(i int) string { return fmt.Sprintf("%s/v%d", canvasOrigin, i) }
