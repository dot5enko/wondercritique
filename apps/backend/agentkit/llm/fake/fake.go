// Package fake provides a scripted llm.Provider for tests.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
)

// ErrNoTurns is returned when the provider is called with an empty script.
var ErrNoTurns = errors.New("fake: no scripted turns left")

// Turn is one scripted model response.
type Turn struct {
	Message    llm.Message // assistant message delivered on message_done (Role defaults to assistant)
	Usage      llm.Usage
	StopReason string // defaults to tool_use when Message has tool calls, else end
	Stream     bool   // emit text and reasoning summaries as word deltas before message_done
	Err        error  // returned instead of a response (after deltas when Stream is set)
	Hang       bool   // block until ctx is cancelled, then return ctx.Err()
	// Check, if set, is called with the request; a non-nil error is returned from the stream.
	Check func(*llm.Request) error
}

// Text is a turn answering with plain text.
func Text(text string) Turn { return Turn{Message: llm.AssistantText(text)} }

// ToolCalls is a turn requesting the given tool calls.
func ToolCalls(calls ...llm.Block) Turn {
	return Turn{Message: llm.NewMessage(llm.RoleAssistant, calls...)}
}

// Call builds a tool_call block with input marshalled to JSON.
func Call(id, name string, input any) llm.Block {
	raw, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return llm.ToolCallBlock(id, name, raw)
}

// Provider replays scripted turns in order and records received requests.
type Provider struct {
	mu       sync.Mutex
	turns    []Turn
	requests []*llm.Request
}

// New returns a provider scripted with turns.
func New(turns ...Turn) *Provider { return &Provider{turns: turns} }

// Push appends turns to the script.
func (p *Provider) Push(turns ...Turn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns = append(p.turns, turns...)
}

// Remaining reports the number of unconsumed turns.
func (p *Provider) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.turns)
}

// Requests returns copies of all requests received so far.
func (p *Provider) Requests() []*llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*llm.Request, len(p.requests))
	for i, r := range p.requests {
		out[i] = cloneRequest(r)
	}
	return out
}

// Name implements llm.Provider.
func (p *Provider) Name() string { return "fake" }

// Stream implements llm.Provider.
func (p *Provider) Stream(ctx context.Context, req *llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.mu.Lock()
		p.requests = append(p.requests, cloneRequest(req))
		if len(p.turns) == 0 {
			p.mu.Unlock()
			yield(llm.StreamEvent{}, ErrNoTurns)
			return
		}
		t := p.turns[0]
		p.turns = p.turns[1:]
		p.mu.Unlock()

		if t.Check != nil {
			if err := t.Check(req); err != nil {
				yield(llm.StreamEvent{}, err)
				return
			}
		}
		msg := t.Message.Clone()
		if msg.Role == "" {
			msg.Role = llm.RoleAssistant
		}
		if t.Stream {
			for _, b := range msg.Blocks {
				var typ llm.StreamEventType
				switch b.Type {
				case llm.BlockText:
					typ = llm.StreamTextDelta
				case llm.BlockReasoning:
					typ = llm.StreamReasoningSummaryDelta
				case llm.BlockToolCall:
					if !yield(llm.StreamEvent{Type: llm.StreamToolCallStart, ToolCallID: b.ToolCallID, ToolName: b.ToolName}, nil) ||
						!yield(llm.StreamEvent{Type: llm.StreamToolCallDelta, ToolCallID: b.ToolCallID, Delta: string(b.Input)}, nil) {
						return
					}
					continue
				default:
					continue
				}
				for _, d := range chunks(b.Text) {
					if !yield(llm.StreamEvent{Type: typ, Delta: d}, nil) {
						return
					}
				}
			}
		}
		if t.Hang {
			<-ctx.Done()
			yield(llm.StreamEvent{}, ctx.Err())
			return
		}
		if t.Err != nil {
			yield(llm.StreamEvent{}, t.Err)
			return
		}
		if err := ctx.Err(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		stop := t.StopReason
		if stop == "" {
			stop = llm.StopEnd
			if len(msg.ToolCalls()) > 0 {
				stop = llm.StopToolUse
			}
		}
		u := t.Usage
		yield(llm.StreamEvent{Type: llm.StreamMessageDone, Message: &msg, Usage: &u, StopReason: stop}, nil)
	}
}

// chunks splits s into word-sized deltas that concatenate back to s.
func chunks(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s[1:], ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func cloneRequest(r *llm.Request) *llm.Request {
	c := *r
	c.Messages = llm.CloneMessages(r.Messages)
	c.Tools = append([]llm.ToolSpec(nil), r.Tools...)
	return &c
}
