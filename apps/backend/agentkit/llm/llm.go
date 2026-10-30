// Package llm defines provider-neutral types for conversations with language models.
package llm

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"strings"
)

// Role is the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// BlockType is the kind of content held by a Block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
	BlockReasoning  BlockType = "reasoning"
)

// PhaseCommentary marks assistant text that precedes a tool call.
const PhaseCommentary = "commentary"

// Block is a single piece of message content.
type Block struct {
	Type       BlockType       `json:"type"`
	Text       string          `json:"text,omitempty"`  // text; reasoning summary
	Phase      string          `json:"phase,omitempty"` // assistant text: "commentary" when it precedes a tool call
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`  // tool_call arguments
	Output     string          `json:"output,omitempty"` // tool_result
	IsError    bool            `json:"is_error,omitempty"`
	ImageURL   string          `json:"image_url,omitempty"` // image (data: URL or https)
	Opaque     json.RawMessage `json:"opaque,omitempty"`    // provider payload replayed verbatim
	Provider   string          `json:"provider,omitempty"`  // provider that produced Opaque
}

// Clone returns a deep copy of b.
func (b Block) Clone() Block {
	b.Input = slices.Clone(b.Input)
	b.Opaque = slices.Clone(b.Opaque)
	return b
}

// TextBlock returns a text block.
func TextBlock(text string) Block { return Block{Type: BlockText, Text: text} }

// ImageBlock returns an image block.
func ImageBlock(url string) Block { return Block{Type: BlockImage, ImageURL: url} }

// ToolCallBlock returns a tool_call block.
func ToolCallBlock(id, name string, input json.RawMessage) Block {
	return Block{Type: BlockToolCall, ToolCallID: id, ToolName: name, Input: input}
}

// ToolResultBlock returns a tool_result block.
func ToolResultBlock(id, name, output string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolCallID: id, ToolName: name, Output: output, IsError: isError}
}

// ReasoningBlock returns a reasoning block carrying an opaque provider payload and an optional summary.
func ReasoningBlock(provider string, opaque json.RawMessage, summary string) Block {
	return Block{Type: BlockReasoning, Provider: provider, Opaque: opaque, Text: summary}
}

// Message is a single conversation turn.
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

// NewMessage builds a message from blocks.
func NewMessage(role Role, blocks ...Block) Message { return Message{Role: role, Blocks: blocks} }

// UserText returns a user message with a single text block.
func UserText(text string) Message { return NewMessage(RoleUser, TextBlock(text)) }

// AssistantText returns an assistant message with a single text block.
func AssistantText(text string) Message { return NewMessage(RoleAssistant, TextBlock(text)) }

// Text concatenates the text blocks of m, separated by newlines.
func (m Message) Text() string {
	var parts []string
	for _, b := range m.Blocks {
		if b.Type == BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ToolCalls returns the tool_call blocks of m.
func (m Message) ToolCalls() []Block { return m.BlocksOf(BlockToolCall) }

// BlocksOf returns the blocks of m with the given type.
func (m Message) BlocksOf(t BlockType) []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == t {
			out = append(out, b)
		}
	}
	return out
}

// Clone returns a deep copy of m.
func (m Message) Clone() Message {
	if m.Blocks != nil {
		bs := make([]Block, len(m.Blocks))
		for i, b := range m.Blocks {
			bs[i] = b.Clone()
		}
		m.Blocks = bs
	}
	return m
}

// CloneMessages deep-copies a message slice.
func CloneMessages(msgs []Message) []Message {
	if msgs == nil {
		return nil
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		out[i] = m.Clone()
	}
	return out
}

// ToolSpec describes a tool offered to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON schema of the input object
}

// Request is a single model invocation.
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolSpec
	MaxOutputTokens int
	ReasoningEffort string
	SafetyID        string
	Extra           map[string]any
}

// Usage is token accounting for one or more model calls.
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens"`
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.CachedInputTokens += o.CachedInputTokens
	u.OutputTokens += o.OutputTokens
	u.ReasoningTokens += o.ReasoningTokens
}

// Total returns input + output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// StreamEventType identifies a StreamEvent.
type StreamEventType string

const (
	StreamTextDelta             StreamEventType = "text_delta"
	StreamReasoningSummaryDelta StreamEventType = "reasoning_summary_delta"
	StreamToolCallStart         StreamEventType = "tool_call_start"
	StreamToolCallDelta         StreamEventType = "tool_call_delta"
	StreamUsage                 StreamEventType = "usage"
	// StreamMessageDone carries the complete assistant message, usage and stop reason.
	// Every successful stream must end with exactly one message_done event.
	StreamMessageDone StreamEventType = "message_done"
)

// Stop reasons reported on message_done.
const (
	StopEnd       = "end"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// StreamEvent is an incremental update from a provider stream.
type StreamEvent struct {
	Type       StreamEventType
	Delta      string // text_delta, reasoning_summary_delta, tool_call_delta (argument JSON fragment)
	ToolCallID string // tool_call_start, tool_call_delta
	ToolName   string // tool_call_start
	Message    *Message
	Usage      *Usage
	StopReason string
}

// Provider is a streaming model backend.
//
// Stream yields deltas and ends with a message_done event containing the complete assistant
// message (reasoning blocks with Opaque, tool_call blocks with full Input). A non-nil error
// terminates the stream.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req *Request) iter.Seq2[StreamEvent, error]
}
