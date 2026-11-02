// SSE consumption for the Anthropic Messages API.
package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/internal/wire"
)

// APIError is a non-200 HTTP response from the Messages API.
type APIError = wire.APIError

// StreamError is a terminal failure reported inside a 200 SSE stream (error event).
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

// consume reads the SSE stream and drives yield.
func (p *Provider) consume(r io.Reader, yield func(llm.StreamEvent, error) bool) error {
	asm := newAssembler(p.cfg.Name, yield)
	err := wire.DrainEvents(r, func(ev wire.Event) error {
		return asm.handle(ev.Name, ev.Data)
	})
	if err != nil && !errors.Is(err, wire.ErrStop) {
		return err
	}
	return nil // message_stop (or EOF) is the natural end; message_done already emitted
}

// event is one Messages API SSE payload.
type event struct {
	Type  string `json:"type"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Message struct {
		ID    string    `json:"id"`
		Usage usageWire `json:"usage"`
	} `json:"message"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		Signature  string `json:"signature"`
		PartialJS0 string `json:"partial_json"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usageWire `json:"usage"` // on message_delta
}

// block is one content_block_start payload.
type block struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input any    `json:"input"` // usually {} for streamed tool uses
	Text  string `json:"text"`
}

// usageWire is the Messages API usage object.
type usageWire struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

func (u usageWire) toLLM() llm.Usage {
	return llm.Usage{
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		OutputTokens:      u.OutputTokens,
	}
}

// pendingBlock accumulates one streamed content block.
type pendingBlock struct {
	typ        string
	toolID     string
	toolName   string
	text       strings.Builder // text or thinking content
	sig        strings.Builder // thinking signature
	args       strings.Builder // tool input JSON fragments
	startInput json.RawMessage // inline input from content_block_start, fallback when no fragments arrived
}

// assembler builds stream events and the final assistant message.
type assembler struct {
	name  string
	yield func(llm.StreamEvent, error) bool

	blocks   map[int]*pendingBlock
	order    []int // block indexes in first-seen order
	input    llm.Usage
	usageSet bool
	stop     string
	done     bool // message_done emitted
	failed   bool // a stream error was already delivered
}

func newAssembler(name string, yield func(llm.StreamEvent, error) bool) *assembler {
	return &assembler{name: name, yield: yield, blocks: map[int]*pendingBlock{}}
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

func (a *assembler) handle(evName, payload string) error {
	var ev event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return fmt.Errorf("%s: bad SSE payload: %w", a.name, err)
	}
	typ := ev.Type
	if typ == "" {
		typ = evName
	}

	switch typ {
	case "error":
		se := &StreamError{Message: "stream error"}
		if ev.Error != nil {
			se.Code, se.Message = ev.Error.Type, ev.Error.Message
		}
		return a.fail(se)
	case "ping":
		return nil
	case "message_start":
		a.input = ev.Message.Usage.toLLM()
		a.usageSet = true
		return nil
	case "content_block_start":
		p := &pendingBlock{typ: "text"}
		var b block
		if json.Unmarshal(ev.ContentBlock, &b) == nil {
			p.typ = b.Type
			p.toolID, p.toolName = b.ID, b.Name
			p.text.WriteString(b.Text)
			if raw, err := json.Marshal(b.Input); err == nil && string(raw) != "null" {
				p.startInput = raw
			}
		}
		a.blocks[ev.Index] = p
		a.order = append(a.order, ev.Index)
		if p.typ == "tool_use" {
			return a.emit(llm.StreamEvent{
				Type: llm.StreamToolCallStart, ToolCallID: p.toolID, ToolName: p.toolName,
			})
		}
		return nil
	case "content_block_delta":
		p := a.blocks[ev.Index]
		if p == nil {
			return nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			p.text.WriteString(ev.Delta.Text)
			if p.typ == "text" || p.typ == "" {
				return a.emit(llm.StreamEvent{Type: llm.StreamTextDelta, Delta: ev.Delta.Text})
			}
		case "thinking_delta":
			p.typ = "thinking"
			p.text.WriteString(ev.Delta.Thinking)
			return a.emit(llm.StreamEvent{Type: llm.StreamReasoningSummaryDelta, Delta: ev.Delta.Thinking})
		case "signature_delta":
			p.sig.WriteString(ev.Delta.Signature)
			return nil
		case "input_json_delta":
			p.typ = "tool_use"
			if frag := ev.Delta.PartialJS0; frag != "" {
				p.args.WriteString(frag)
				return a.emit(llm.StreamEvent{
					Type: llm.StreamToolCallDelta, ToolCallID: p.toolID, Delta: frag,
				})
			}
		}
		return nil
	case "content_block_stop":
		return nil
	case "message_delta":
		if ev.Usage != nil {
			out := ev.Usage.toLLM()
			a.input.OutputTokens += out.OutputTokens
			a.input.CachedInputTokens += out.CachedInputTokens
		}
		if ev.Delta.StopReason != "" {
			a.stop = ev.Delta.StopReason
		}
		return nil
	case "message_stop":
		if a.failed || a.done {
			return wire.ErrStop
		}
		a.done = true
		msg, stop := a.buildMessage()
		u := a.input
		return a.emit(llm.StreamEvent{
			Type: llm.StreamMessageDone, Message: &msg, Usage: &u, StopReason: stop,
		})
	}
	return nil
}

// buildMessage assembles the assistant llm.Message from the accumulated blocks.
func (a *assembler) buildMessage() (llm.Message, string) {
	msg := llm.Message{Role: llm.RoleAssistant}
	stop := llm.StopEnd
	switch a.stop {
	case "tool_use":
		stop = llm.StopToolUse
	case "max_tokens":
		stop = llm.StopMaxTokens
	case "end_turn", "stop_sequence":
		stop = llm.StopEnd
	}
	for _, idx := range a.order {
		p := a.blocks[idx]
		if p == nil {
			continue
		}
		switch p.typ {
		case "thinking":
			opaque, _ := json.Marshal(thinkingOpaque{Thinking: p.text.String(), Signature: p.sig.String()})
			msg.Blocks = append(msg.Blocks, llm.ReasoningBlock(a.name, opaque, p.text.String()))
		case "tool_use":
			args := json.RawMessage(p.args.String())
			if !json.Valid(args) {
				args = p.startInput
			}
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			msg.Blocks = append(msg.Blocks, llm.ToolCallBlock(p.toolID, p.toolName, args))
			if stop == llm.StopEnd {
				stop = llm.StopToolUse
			}
		default: // text
			if t := p.text.String(); t != "" {
				msg.Blocks = append(msg.Blocks, llm.TextBlock(t))
			}
		}
	}
	return msg, stop
}
