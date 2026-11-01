package render

import (
	"errors"
	"strings"
	"testing"
)

// TestChromeFlags pins item (f): the sandbox stays on unless explicitly
// disabled, and the proxy pair is always present.
func TestChromeFlags(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	flags := c.chromeFlags("http://127.0.0.1:41234")
	if flags["no-sandbox"] != false {
		t.Error("no-sandbox must default to false")
	}
	if flags["proxy-server"] != "http://127.0.0.1:41234" {
		t.Errorf("proxy-server = %v", flags["proxy-server"])
	}
	if flags["proxy-bypass-list"] != "<-loopback>" {
		t.Errorf("proxy-bypass-list = %v", flags["proxy-bypass-list"])
	}
	features, _ := flags["disable-features"].(string)
	for _, want := range []string{"Translate", "BlinkGenPropertyTrees", "OptimizationHints", "MediaRouter"} {
		if !strings.Contains(features, want) {
			t.Errorf("disable-features missing %s: %q", want, features)
		}
	}
	if flags["disable-component-update"] != true || flags["no-ping"] != true {
		t.Errorf("privacy flags: %v", flags)
	}

	optOut, err := New(Config{NoSandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	if optOut.chromeFlags("")["no-sandbox"] != true {
		t.Error("NoSandbox=true must add --no-sandbox")
	}
}

func TestFindChromeExplicitPath(t *testing.T) {
	if _, err := findChrome("/definitely/not/a/browser"); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("err = %v, want ErrNoBrowser", err)
	}
}
