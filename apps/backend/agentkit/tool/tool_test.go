package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

type addIn struct {
	A     int      `json:"a" jsonschema:"description=first operand"`
	B     int      `json:"b"`
	Note  string   `json:"note,omitempty"`
	Inner struct { // nested structs must be inlined too
		X []string `json:"x"`
	} `json:"inner,omitempty"`
}

type addOut struct {
	Sum int `json:"sum"`
}

func newAdd() tool.Tool {
	return tool.New("add", "Adds numbers", func(_ context.Context, in addIn) (addOut, error) {
		return addOut{Sum: in.A + in.B}, nil
	})
}

func TestSchema(t *testing.T) {
	spec := newAdd().Spec()
	if spec.Name != "add" || spec.Description != "Adds numbers" {
		t.Fatalf("spec = %+v", spec)
	}
	s := string(spec.Schema)
	for _, bad := range []string{"$ref", "$schema", "$id", "$defs"} {
		if strings.Contains(s, bad) {
			t.Errorf("schema contains %s: %s", bad, s)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(spec.Schema, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" || m["additionalProperties"] != false {
		t.Errorf("schema = %s", s)
	}
	req, _ := m["required"].([]any)
	if len(req) != 2 || req[0] != "a" || req[1] != "b" {
		t.Errorf("required = %v", req)
	}
	props := m["properties"].(map[string]any)
	if props["a"].(map[string]any)["description"] != "first operand" {
		t.Errorf("description missing: %s", s)
	}
	inner := props["inner"].(map[string]any)
	if inner["type"] != "object" || inner["additionalProperties"] != false {
		t.Errorf("nested struct not inlined: %s", s)
	}
}

func TestCall(t *testing.T) {
	ctx := context.Background()
	add := newAdd()
	out, err := add.Call(ctx, json.RawMessage(`{"a":2,"b":3}`))
	if err != nil || out != `{"sum":5}` {
		t.Fatalf("Call = %q, %v", out, err)
	}
	if _, err := add.Call(ctx, json.RawMessage(`{"a":2,"c":3}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field err = %v", err)
	}
	if _, err := add.Call(ctx, json.RawMessage(`{"a":"x"}`)); err == nil {
		t.Fatal("type mismatch accepted")
	}
	if _, err := add.Call(ctx, json.RawMessage(`{"a":1} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if out, err := add.Call(ctx, nil); err != nil || out != `{"sum":0}` {
		t.Fatalf("empty input = %q, %v", out, err)
	}

	echo := tool.New("echo", "", func(_ context.Context, in struct {
		S string `json:"s"`
	}) (string, error) {
		return in.S, nil
	})
	if out, _ := echo.Call(ctx, json.RawMessage(`{"s":"raw \"text\""}`)); out != `raw "text"` {
		t.Fatalf("string output encoded: %q", out)
	}
}

func TestNewPanicsOnNonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	tool.New("bad", "", func(context.Context, string) (string, error) { return "", nil })
}

func TestSet(t *testing.T) {
	var nilSet *tool.Set
	if _, ok := nilSet.Get("x"); ok || nilSet.Specs() != nil || nilSet.Len() != 0 {
		t.Fatal("nil set not empty")
	}
	echo := tool.Func{ToolSpec: tool.New("echo", "v1", func(context.Context, struct{}) (string, error) { return "", nil }).Spec()}
	s := tool.NewSet(newAdd(), echo)
	echo.ToolSpec.Description = "v2"
	s.Add(echo)
	specs := s.Specs()
	if len(specs) != 2 || specs[0].Name != "add" || specs[1].Name != "echo" || specs[1].Description != "v2" {
		t.Fatalf("specs = %+v", specs)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, ok := s.Get("add"); !ok {
				t.Error("add missing")
			}
			_ = s.Specs()
		})
	}
	wg.Wait()
}
