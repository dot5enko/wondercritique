// Wire-format helpers: request building (Chat Completions) and SSE consumption.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/internal/wire"
)

// APIError is a non-200 HTTP response from a Chat Completions endpoint.
type APIError = wire.APIError

// StreamError is a terminal failure reported inside a 200 SSE stream.
type StreamError struct {
	Code    string
	Message string
}

func (e *StreamError) Error() string {
	s := "stream error"
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// reasoningOpaque is the Opaque payload stored on reasoning blocks: the raw
// provider reasoning content, replayed as the assistant message's
// reasoning_content field on subsequent requests.
type reasoningOpaque struct {
	ReasoningContent string `json:"reasoning_content"`
}

// buildBody maps an llm.Request to the Chat Completions request body.
func buildBody(req *llm.Request, defaultModel, name string) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	msgs, err := buildMessages(req, name)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":          model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       msgs,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			params := json.RawMessage(t.Schema)
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": params,
				},
			}
		}
		body["tools"] = tools
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = strings.ToLower(req.ReasoningEffort)
	}
	if req.MaxOutputTokens > 0 {
		body["max_tokens"] = req.MaxOutputTokens
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

// buildMessages maps conversation history to Chat Completions messages.
func buildMessages(req *llm.Request, name string) ([]map[string]any, error) {
	var msgs []map[string]any
	if req.System != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleUser:
			msgs = append(msgs, userMessage(m))
		case llm.RoleAssistant:
			am, err := assistantMessage(m, name)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, am)
		case llm.RoleTool:
			for _, b := range m.Blocks {
				if b.Type != llm.BlockToolResult {
					continue
				}
				msgs = append(msgs, map[string]any{
					"role": "tool", "tool_call_id": b.ToolCallID, "content": b.Output,
				})
			}
		}
	}
	return msgs, nil
}

// userMessage maps a user message: a lone text block becomes a string content,
// mixed content becomes a multipart array.
func userMessage(m llm.Message) map[string]any {
	var texts []string
	var parts []map[string]any
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockText:
			texts = append(texts, b.Text)
			parts = append(parts, map[string]any{"type": "text", "text": b.Text})
		case llm.BlockImage:
			parts = append(parts, map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": b.ImageURL},
			})
		}
	}
	if len(parts) == 0 {
		return map[string]any{"role": "user", "content": ""}
	}
	if len(parts) == len(texts) { // text only
		return map[string]any{"role": "user", "content": strings.Join(texts, "\n")}
	}
	return map[string]any{"role": "user", "content": parts}
}

// assistantMessage maps an assistant message: text, replayable reasoning
// content and tool calls.
func assistantMessage(m llm.Message, name string) (map[string]any, error) {
	var text []string
	for _, b := range m.Blocks {
		if b.Type == llm.BlockText {
			text = append(text, b.Text)
		}
	}
	out := map[string]any{"role": "assistant"}
	content := strings.Join(text, "\n")
	if len(m.ToolCalls()) == 0 {
		out["content"] = content
	} else if content != "" {
		out["content"] = content
	} else {
		out["content"] = nil
	}
	for _, b := range m.Blocks {
		if b.Type != llm.BlockReasoning {
			continue
		}
		if b.Provider != "" && b.Provider != name {
			continue // reasoning from another provider is not replayable here
		}
		var o reasoningOpaque
		if len(b.Opaque) == 0 || json.Unmarshal(b.Opaque, &o) != nil || o.ReasoningContent == "" {
			continue // nothing replayable
		}
		out["reasoning_content"] = o.ReasoningContent
		break
	}
	if calls := m.ToolCalls(); len(calls) > 0 {
		tcs := make([]map[string]any, len(calls))
		for i, b := range calls {
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			tcs[i] = map[string]any{
				"id": b.ToolCallID, "type": "function",
				"function": map[string]any{"name": b.ToolName, "arguments": string(args)},
			}
		}
		out["tool_calls"] = tcs
	}
	return out, nil
}

// consume reads the SSE stream and drives yield.
func (p *Provider) consume(r io.Reader, yield func(llm.StreamEvent, error) bool) error {
	asm := newAssembler(p.cfg.Name, yield)
	err := wire.DrainEvents(r, func(ev wire.Event) error {
		if ev.Done() {
			return wire.ErrStop
		}
		return asm.handle(ev.Data)
	})
	if err != nil && !errors.Is(err, wire.ErrStop) {
		return err
	}
	return asm.finish()
}

// deltaToolCall is a streaming tool call fragment.
type deltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chunk is one SSE data payload.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []deltaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason any `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageWire `json:"usage"`
	Error *struct {
		Code    any     `json:"code"`
		Message string  `json:"message"`
		Param   *string `json:"param"`
	} `json:"error"`
}

// usageWire is the Chat Completions usage object.
type usageWire struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *usageWire) toLLM() llm.Usage {
	return llm.Usage{
		InputTokens:       u.PromptTokens,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
		OutputTokens:      u.CompletionTokens,
		ReasoningTokens:   u.CompletionTokensDetails.ReasoningTokens,
	}
}

// pendingToolCall accumulates one streamed tool call.
type pendingToolCall struct {
	id, name string
	args     strings.Builder
	started  bool
}

// assembler builds stream events and the final assistant message.
type assembler struct {
	name  string
	yield func(llm.StreamEvent, error) bool

	text      strings.Builder
	reasoning strings.Builder
	tools     map[int]*pendingToolCall
	order     []int // tool call indexes in first-seen order
	stop      any   // finish_reason from the last chunk carrying one
	usage     *usageWire
	failed    bool // a stream error was already delivered
}

func newAssembler(name string, yield func(llm.StreamEvent, error) bool) *assembler {
	return &assembler{name: name, yield: yield, tools: map[int]*pendingToolCall{}}
}

func (a *assembler) emit(ev llm.StreamEvent) error {
	if !a.yield(ev, nil) {
		return wire.ErrStop
	}
	return nil
}

func (a *assembler) fail(err error) error {
	a.failed = true
	a.yield(llm.StreamEvent{}, fmt.Errorf("%s: %w", a.name, err))
	return wire.ErrStop // the error was delivered; stop silently
}

func (a *assembler) handle(data string) error {
	var ch chunk
	if err := json.Unmarshal([]byte(data), &ch); err != nil {
		return fmt.Errorf("%s: bad SSE payload: %w", a.name, err)
	}
	if ch.Error != nil {
		se := &StreamError{Message: ch.Error.Message}
		switch c := ch.Error.Code.(type) {
		case string:
			se.Code = c
		case float64:
			se.Code = fmt.Sprintf("%v", c)
		}
		return a.fail(se)
	}
	for _, c := range ch.Choices {
		d := c.Delta
		if d.Content != "" {
			a.text.WriteString(d.Content)
			if err := a.emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: d.Content}); err != nil {
				return err
			}
		}
		if d.ReasoningContent != "" {
			a.reasoning.WriteString(d.ReasoningContent)
			if err := a.emit(llm.StreamEvent{Type: llm.StreamReasoningSummaryDelta, Delta: d.ReasoningContent}); err != nil {
				return err
			}
		}
		for _, tc := range d.ToolCalls {
			p := a.tools[tc.Index]
			if p == nil {
				p = &pendingToolCall{}
				a.tools[tc.Index] = p
				a.order = append(a.order, tc.Index)
			}
			if tc.ID != "" {
				p.id = tc.ID
			}
			if tc.Function.Name != "" {
				p.name += tc.Function.Name // some providers stream the name in fragments
			}
			if frag := tc.Function.Arguments; frag != "" {
				p.args.WriteString(frag)
				if p.started {
					if err := a.emit(llm.StreamEvent{
						Type: llm.StreamToolCallDelta, ToolCallID: p.id, Delta: frag,
					}); err != nil {
						return err
					}
				}
			}
			if !p.started && p.id != "" && p.name != "" {
				p.started = true
				if err := a.emit(llm.StreamEvent{
					Type: llm.StreamToolCallStart, ToolCallID: p.id, ToolName: p.name,
				}); err != nil {
					return err
				}
			}
		}
		if c.FinishReason != nil {
			a.stop = c.FinishReason
		}
	}
	if ch.Usage != nil {
		a.usage = ch.Usage
	}
	return nil
}

// finish assembles the final message_done event. Called after the stream ends.
func (a *assembler) finish() error {
	if a.failed {
		return wire.ErrStop // the error was already delivered
	}
	msg := llm.Message{Role: llm.RoleAssistant}
	if r := a.reasoning.String(); r != "" {
		opaque, _ := json.Marshal(reasoningOpaque{ReasoningContent: r})
		msg.Blocks = append(msg.Blocks, llm.ReasoningBlock(a.name, opaque, r))
	}
	if t := a.text.String(); t != "" {
		msg.Blocks = append(msg.Blocks, llm.TextBlock(t))
	}
	stop := llm.StopEnd
	for _, idx := range a.order {
		p := a.tools[idx]
		if p == nil || p.id == "" {
			continue // a tool call that never got an id is unusable
		}
		args := json.RawMessage(p.args.String())
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		msg.Blocks = append(msg.Blocks, llm.ToolCallBlock(p.id, p.name, args))
		stop = llm.StopToolUse
	}
	switch a.stop {
	case "tool_calls":
		stop = llm.StopToolUse
	case "length":
		stop = llm.StopMaxTokens
	case "max_tokens":
		stop = llm.StopMaxTokens
	}
	u := llm.Usage{}
	if a.usage != nil {
		u = a.usage.toLLM()
	}
	return a.emit(llm.StreamEvent{
		Type: llm.StreamMessageDone, Message: &msg, Usage: &u, StopReason: stop,
	})
}
