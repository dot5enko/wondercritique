// Wire-format helpers: request building (Responses API, stateless encrypted
// reasoning replay) and SSE consumption.
package responses

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/internal/wire"
)

// APIError is a non-200 HTTP response from a Responses API endpoint.
type APIError = wire.APIError

// StreamError is a terminal failure reported inside a 200 SSE stream (response.failed or error event).
type StreamError struct {
	Event   string // "response.failed" or "error"
	Code    string // e.g. server_error, rate_limit_exceeded, server_shutting_down
	Message string
	Param   string
}

func (e *StreamError) Error() string {
	s := e.Event
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// effortLevels are accepted values of reasoning.effort. "none" is rejected by Muse Spark.
var effortLevels = map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

// buildBody maps an llm.Request to the Responses API request body. name is the
// provider identity used for reasoning-block replay validation and error messages.
func buildBody(req *llm.Request, defaultModel, name string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if req.ReasoningEffort != "" {
		if strings.EqualFold(req.ReasoningEffort, "none") {
			return nil, fmt.Errorf("%s: reasoning effort %q is not supported", name, req.ReasoningEffort)
		}
		if !effortLevels[strings.ToLower(req.ReasoningEffort)] {
			return nil, fmt.Errorf("%s: invalid reasoning effort %q", name, req.ReasoningEffort)
		}
	}
	input, err := buildInput(req.Messages, name)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":   model,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
		"input":   input,
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			params := json.RawMessage(t.Schema)
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools[i] = map[string]any{
				"type": "function", "name": t.Name, "description": t.Description, "parameters": params,
			}
		}
		body["tools"] = tools
	}
	if req.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": strings.ToLower(req.ReasoningEffort)}
	}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.SafetyID != "" {
		body["safety_identifier"] = req.SafetyID
	}
	for k, v := range req.Extra {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}
	return json.Marshal(body)
}

// BuildInput maps conversation history to Responses API input items, using the
// default provider identity. Exported for hosts that need to preview what a
// request will contain.
func BuildInput(messages []llm.Message) ([]map[string]any, error) {
	return buildInput(messages, DefaultName)
}

// BuildInputAs maps conversation history to Responses API input items using
// name as the provider identity: reasoning blocks tagged with that name are
// replayed verbatim, blocks tagged otherwise are skipped.
func BuildInputAs(name string, messages []llm.Message) ([]map[string]any, error) {
	return buildInput(messages, name)
}

// buildInput maps conversation history to Responses API input items. name is
// the provider identity whose reasoning blocks are replayable.
func buildInput(messages []llm.Message, name string) ([]map[string]any, error) {
	var items []map[string]any
	needsAssistant := false // a reasoning item must be followed by an assistant message or function_call
	appendAssistant := func() {
		items = append(items, map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": ""}},
		})
		needsAssistant = false
	}
	for _, m := range messages {
		if needsAssistant && (m.Role == llm.RoleUser || m.Role == llm.RoleSystem) {
			appendAssistant()
		}
		switch m.Role {
		case llm.RoleUser:
			var content []map[string]any
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockText:
					content = append(content, map[string]any{"type": "input_text", "text": b.Text})
				case llm.BlockImage:
					content = append(content, map[string]any{"type": "input_image", "image_url": b.ImageURL})
				}
			}
			if len(content) == 0 {
				content = []map[string]any{{"type": "input_text", "text": m.Text()}}
			}
			items = append(items, map[string]any{"type": "message", "role": "user", "content": content})
			needsAssistant = false
		case llm.RoleSystem:
			items = append(items, map[string]any{"type": "message", "role": "system",
				"content": []map[string]any{{"type": "input_text", "text": m.Text()}}})
			needsAssistant = false
		case llm.RoleAssistant:
			hasCalls := len(m.ToolCalls()) > 0
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockReasoning:
					if b.Provider != "" && b.Provider != name {
						continue // reasoning from another provider is not replayable here
					}
					opaque, err := replayReasoning(b.Opaque, name)
					if err != nil {
						return nil, err
					}
					items = append(items, opaque)
					needsAssistant = true
				case llm.BlockText:
					phase := b.Phase
					if hasCalls && phase == "" {
						phase = llm.PhaseCommentary // text before a tool call must be commentary
					}
					item := map[string]any{
						"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": b.Text}},
					}
					if phase != "" {
						item["phase"] = phase
					}
					items = append(items, item)
					needsAssistant = false
				case llm.BlockToolCall:
					args := b.Input
					if len(args) == 0 {
						args = json.RawMessage(`{}`)
					}
					items = append(items, map[string]any{
						"type": "function_call", "call_id": callID(b.ToolCallID), "name": b.ToolName,
						"arguments": string(args),
					})
					needsAssistant = false
				}
			}
		case llm.RoleTool:
			for _, b := range m.Blocks {
				if b.Type != llm.BlockToolResult {
					continue
				}
				items = append(items, map[string]any{
					"type": "function_call_output", "call_id": callID(b.ToolCallID), "output": b.Output,
				})
				needsAssistant = false
			}
		}
	}
	if needsAssistant {
		appendAssistant()
	}
	return items, nil
}

// ProviderIdentity is unused; replay identity is threaded through buildInput.

// replayReasoning returns the reasoning item to send as input: the opaque
// payload verbatim, with a summary key ensured.
func replayReasoning(opaque json.RawMessage, name string) (map[string]any, error) {
	if len(opaque) == 0 {
		return nil, errors.New(name + ": reasoning block without opaque payload cannot be replayed")
	}
	var item map[string]any
	if err := json.Unmarshal(opaque, &item); err != nil {
		return nil, fmt.Errorf("%s: invalid reasoning payload: %w", name, err)
	}
	if _, ok := item["summary"]; !ok {
		item["summary"] = []any{}
	}
	return item, nil
}

// callID keeps function_call ids within the 1-64 char API limit, deterministically.
func callID(id string) string {
	if id == "" {
		return "call"
	}
	if len(id) <= 64 {
		return id
	}
	return "mk_" + fmt.Sprintf("%x", sha256.Sum256([]byte(id)))[:16]
}

// consume reads an SSE stream and drives yield. It returns wire.ErrStop when
// the consumer stopped, and other errors for stream failures.
func (p *Provider) consume(r io.Reader, yield func(llm.StreamEvent, error) bool) error {
	asm := newAssembler(p.cfg.Name, yield)
	return wire.DrainEvents(r, func(ev wire.Event) error {
		if ev.Done() {
			return wire.ErrStop
		}
		return asm.handle(ev.Name, ev.Data)
	})
}

// item is a decoded Responses API output item.
type item struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Phase   string `json:"phase"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	ID        string `json:"id"`
}

// assembler builds the final assistant message from the response's output items.
type assembler struct {
	prefix string // provider name used in error strings
	yield  func(llm.StreamEvent, error) bool
	seen   bool // any output received at all
}

func newAssembler(name string, yield func(llm.StreamEvent, error) bool) *assembler {
	return &assembler{prefix: name, yield: yield}
}

func (a *assembler) handle(event, payload string) error {
	var envelope struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Code    string  `json:"code"`
			Message string  `json:"message"`
			Param   *string `json:"param"`
		} `json:"error"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return fmt.Errorf("%s: bad SSE payload: %w", a.prefix, err)
	}
	typ := envelope.Type
	if typ == "" {
		typ = event
	}

	switch typ {
	case "response.output_text.delta":
		return a.emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: envelope.Delta})
	case "response.reasoning_summary_text.delta":
		return a.emit(llm.StreamEvent{Type: llm.StreamReasoningSummaryDelta, Delta: envelope.Delta})
	case "response.output_item.added":
		var it item
		if json.Unmarshal(envelope.Item, &it) == nil && it.Type == "function_call" {
			return a.emit(llm.StreamEvent{
				Type: llm.StreamToolCallStart, ToolCallID: callID(it.CallID), ToolName: it.Name,
			})
		}
	case "response.function_call_arguments.delta":
		var d struct {
			CallID string `json:"call_id"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal([]byte(payload), &d) == nil {
			return a.emit(llm.StreamEvent{
				Type: llm.StreamToolCallDelta, ToolCallID: callID(d.CallID), Delta: d.Delta,
			})
		}
	case "response.failed", "response.error", "error":
		if envelope.Error != nil {
			se := &StreamError{Event: typ, Code: envelope.Error.Code, Message: envelope.Error.Message}
			if envelope.Error.Param != nil {
				se.Param = *envelope.Error.Param
			}
			return a.fail(se)
		}
		// response.failed nests the error inside the response object.
		var failed struct {
			Response struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if typ == "response.failed" && json.Unmarshal(envelope.Response, &failed.Response) == nil && failed.Response.Error != nil {
			return a.fail(&StreamError{Event: typ, Code: failed.Response.Error.Code, Message: failed.Response.Error.Message})
		}
		var se StreamError
		if json.Unmarshal([]byte(payload), &se) == nil && se.Message != "" {
			se.Event = typ
			return a.fail(&se)
		}
		return a.fail(&StreamError{Event: typ, Message: payload})
	case "response.completed", "response.incomplete":
		var body struct {
			Response struct {
				Status            string            `json:"status"`
				Output            []json.RawMessage `json:"output"`
				Usage             *usageWire        `json:"usage"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal(envelope.Response, &body.Response); err != nil {
			return fmt.Errorf("%s: bad response envelope: %w", a.prefix, err)
		}
		if body.Response.Error != nil && body.Response.Error.Message != "" {
			return a.fail(&StreamError{Event: typ, Code: body.Response.Error.Code, Message: body.Response.Error.Message})
		}
		msg, stop, err := a.buildMessage(body.Response.Output)
		if err != nil {
			return err
		}
		u := llm.Usage{}
		if body.Response.Usage != nil {
			u = body.Response.Usage.toLLM()
		}
		if body.Response.IncompleteDetails != nil && body.Response.IncompleteDetails.Reason == "max_output_tokens" {
			stop = llm.StopMaxTokens
		}
		a.seen = true
		return a.emit(llm.StreamEvent{
			Type: llm.StreamMessageDone, Message: &msg, Usage: &u, StopReason: stop,
		})
	}
	return nil
}

func (a *assembler) emit(ev llm.StreamEvent) error {
	if !a.yield(ev, nil) {
		return wire.ErrStop
	}
	return nil
}

func (a *assembler) fail(err error) error {
	a.yield(llm.StreamEvent{}, fmt.Errorf("%s: %w", a.prefix, err))
	return wire.ErrStop // the error was delivered; stop silently
}

// buildMessage assembles the assistant llm.Message from output items in order.
func (a *assembler) buildMessage(output []json.RawMessage) (llm.Message, string, error) {
	msg := llm.Message{Role: llm.RoleAssistant}
	stop := llm.StopEnd
	for _, raw := range output {
		var it item
		if err := json.Unmarshal(raw, &it); err != nil {
			return msg, stop, fmt.Errorf("%s: bad output item: %w", a.prefix, err)
		}
		switch it.Type {
		case "reasoning":
			var summary strings.Builder
			for _, s := range it.Summary {
				summary.WriteString(s.Text)
			}
			msg.Blocks = append(msg.Blocks, llm.ReasoningBlock(a.prefix, raw, summary.String()))
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" {
					msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: c.Text, Phase: it.Phase})
				}
			}
		case "function_call":
			args := json.RawMessage(it.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			msg.Blocks = append(msg.Blocks, llm.ToolCallBlock(callID(it.CallID), it.Name, args))
			stop = llm.StopToolUse
		}
	}
	return msg, stop, nil
}

// usageWire is the Responses API usage object.
type usageWire struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *usageWire) toLLM() llm.Usage {
	return llm.Usage{
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.InputTokensDetails.CachedTokens,
		OutputTokens:      u.OutputTokens,
		ReasoningTokens:   u.OutputTokensDetails.ReasoningTokens,
	}
}
