package jsrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func callTool(t *testing.T, r *Runner, code string) (string, error) {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"code": code})
	return NewTool(r, newFS(t, map[string]string{"/work/a.md": "# A\n"})).Call(context.Background(), in)
}

func TestToolSpec(t *testing.T) {
	r := New(Config{})
	spec := NewTool(r, newFS(t, nil)).Spec()
	if spec.Name != "run_js" {
		t.Fatalf("name = %q", spec.Name)
	}
	for _, part := range []string{"5s wall time", "64 MiB memory", "32 KiB console output", "about 100 calls (about 50 through callbacks", "fs.read(path)", "EROFS"} {
		if !strings.Contains(spec.Description, part) {
			t.Errorf("description lacks %q:\n%s", part, spec.Description)
		}
	}
	if !strings.Contains(string(spec.Schema), `"code"`) || !strings.Contains(string(spec.Schema), `"required":["code"]`) {
		t.Errorf("schema = %s", spec.Schema)
	}
	if noFS := NewTool(r, nil).Spec().Description; strings.Contains(noFS, "fs.read") {
		t.Errorf("description without FS mentions fs:\n%s", noFS)
	}
}

func TestToolOutput(t *testing.T) {
	r := New(Config{})

	out, err := callTool(t, r, `console.log("hi"); ({title: fs.read("/work/a.md").trim()})`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "console:\nhi\n\nresult: {\"title\":\"# A\"}\n(") {
		t.Fatalf("output = %q", out)
	}

	out, err = callTool(t, r, `let x = 1;`)
	if err != nil || !strings.HasPrefix(out, "result: undefined\n(") {
		t.Fatalf("output = %q, %v", out, err)
	}

	out, err = callTool(t, r, "function f() { throw new Error(\"nope\") }\nf()")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "error (exception): Error: nope\n    at f (script.js:1:") {
		t.Fatalf("output = %q", out)
	}

	// Invalid input is a tool error, without the package prefix.
	if _, err := callTool(t, r, " "); err == nil || err.Error() != "code is empty" {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewTool(r, nil).Call(context.Background(), json.RawMessage(`{"code":"1","timeout":5}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestFormatResult(t *testing.T) {
	cfg := Config{}.WithDefaults()
	got := FormatResult(&Result{
		Output:          "partial",
		OutputTruncated: true,
		Error:           &Error{Kind: KindTimeout, Message: "the script did not finish within 5s"},
		Duration:        5001 * time.Millisecond,
	}, cfg)
	want := "console:\npartial\n[console output truncated at 32768 bytes]\n\nerror (timeout): the script did not finish within 5s\n(5.001s)"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
