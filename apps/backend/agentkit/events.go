package agentkit

import (
	"encoding/json"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
)

// EventType identifies an Event.
type EventType string

const (
	EventRunStarted       EventType = "run_started"
	EventTextDelta        EventType = "text_delta"
	EventReasoningDelta   EventType = "reasoning_delta"
	EventToolCallStarted  EventType = "tool_call_started"
	EventToolCallFinished EventType = "tool_call_finished"
	EventStepFinished     EventType = "step_finished"
	EventRunFinished      EventType = "run_finished"
	EventRunFailed        EventType = "run_failed" // also emitted for cancelled runs (Status cancelled)
)

// Event is a progress notification from a run. Fields are populated per type:
//   - text_delta, reasoning_delta: Delta
//   - tool_call_started, tool_call_finished: ToolCall
//   - step_finished: Message (assistant), Usage (this step), StopReason
//   - run_finished: Text (final answer), Usage (run total), Status
//   - run_failed: Error, Usage (run total), Status (failed or cancelled)
type Event struct {
	Type       EventType       `json:"type"`
	SessionID  string          `json:"session_id"`
	RunID      string          `json:"run_id,omitempty"`
	Step       int             `json:"step,omitempty"`
	Delta      string          `json:"delta,omitempty"`
	ToolCall   *ToolCallEvent  `json:"tool_call,omitempty"`
	Message    *llm.Message    `json:"message,omitempty"`
	Usage      *llm.Usage      `json:"usage,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Text       string          `json:"text,omitempty"`
	Status     store.RunStatus `json:"status,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// ToolCallEvent describes a tool call in tool_call_* events. Output, IsError and Duration are set
// on tool_call_finished.
type ToolCallEvent struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`
	IsError  bool            `json:"is_error,omitempty"`
	Duration time.Duration   `json:"duration,omitempty"`
}
