// Package tool defines tools callable by agents and typed helpers to build them.
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/invopop/jsonschema"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
)

// Tool is a function the model can call.
type Tool interface {
	Spec() llm.ToolSpec
	Call(ctx context.Context, input json.RawMessage) (string, error)
}

// Func is a Tool built from a spec and a raw handler.
type Func struct {
	ToolSpec llm.ToolSpec
	Fn       func(ctx context.Context, input json.RawMessage) (string, error)
}

// Spec implements Tool.
func (f Func) Spec() llm.ToolSpec { return f.ToolSpec }

// Call implements Tool.
func (f Func) Call(ctx context.Context, input json.RawMessage) (string, error) {
	return f.Fn(ctx, input)
}

// New builds a typed tool. The input schema is generated from In, which must be a struct
// (use `json` and `jsonschema` struct tags for names, descriptions and constraints). Input is
// decoded strictly (unknown fields rejected). Out is JSON-encoded unless it is a string.
// New panics if In is not a struct, since that is a programming error.
func New[In, Out any](name, description string, fn func(ctx context.Context, in In) (Out, error)) Tool {
	return Func{
		ToolSpec: llm.ToolSpec{Name: name, Description: description, Schema: SchemaFor[In]()},
		Fn: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in In
			if err := decodeStrict(raw, &in); err != nil {
				return "", fmt.Errorf("invalid input for tool %s: %w", name, err)
			}
			out, err := fn(ctx, in)
			if err != nil {
				return "", err
			}
			if s, ok := any(out).(string); ok {
				return s, nil
			}
			b, err := json.Marshal(out)
			if err != nil {
				return "", fmt.Errorf("encode output of tool %s: %w", name, err)
			}
			return string(b), nil
		},
	}
}

// SchemaFor returns the inline JSON schema for struct type T (no $ref, $schema or $id;
// additionalProperties false).
func SchemaFor[T any]() json.RawMessage {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("tool: input type %s must be a struct", t))
	}
	r := jsonschema.Reflector{DoNotReference: true, ExpandedStruct: true, Anonymous: true}
	s := r.ReflectFromType(t)
	s.Version = ""
	s.ID = ""
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("tool: schema for %s: %v", t, err))
	}
	return b
}

func decodeStrict(raw json.RawMessage, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected data after JSON object")
	}
	return nil
}

// Set is an ordered collection of tools keyed by name, safe for concurrent use.
// A nil *Set is an empty set.
type Set struct {
	mu    sync.RWMutex
	order []string
	tools map[string]Tool
}

// NewSet returns a set containing tools.
func NewSet(tools ...Tool) *Set {
	s := &Set{tools: map[string]Tool{}}
	s.Add(tools...)
	return s
}

// Add registers tools; a tool replaces an existing one with the same name (keeping its position).
func (s *Set) Add(tools ...Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range tools {
		name := t.Spec().Name
		if _, ok := s.tools[name]; !ok {
			s.order = append(s.order, name)
		}
		s.tools[name] = t
	}
}

// Get returns the tool with the given name.
func (s *Set) Get(name string) (Tool, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tools[name]
	return t, ok
}

// Specs returns the specs of all tools in registration order.
func (s *Set) Specs() []llm.ToolSpec {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]llm.ToolSpec, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.tools[n].Spec())
	}
	return out
}

// Len returns the number of tools.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.order)
}
