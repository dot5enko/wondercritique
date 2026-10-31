// Package store defines persistence for agent sessions, messages, runs and tool calls.
package store

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
)

// ErrNotFound is returned when a session or run does not exist.
var ErrNotFound = errors.New("agentkit/store: not found")

// Session is a persistent conversation owned by an agent.
type Session struct {
	ID        string         `json:"id"`
	Agent     string         `json:"agent"`
	Title     string         `json:"title"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// StoredMessage is a message persisted in a session.
type StoredMessage struct {
	ID        string      `json:"id"`
	SessionID string      `json:"session_id"`
	RunID     string      `json:"run_id"`
	Seq       int64       `json:"seq"` // 1-based, strictly increasing within a session
	Message   llm.Message `json:"message"`
	CreatedAt time.Time   `json:"created_at"`
}

// RunStatus is the lifecycle state of a run.
type RunStatus string

const (
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted" // was running when the process stopped
)

// Run is one execution of the agent loop in a session.
type Run struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Agent      string    `json:"agent"`
	Model      string    `json:"model"`
	Status     RunStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	Usage      llm.Usage `json:"usage"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"` // zero while running
}

// ToolCallRecord is an audit record of one tool execution.
type ToolCallRecord struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	SessionID string          `json:"session_id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Output    string          `json:"output"`
	IsError   bool            `json:"is_error"`
	Duration  time.Duration   `json:"duration"`
	CreatedAt time.Time       `json:"created_at"`
}

// ListOptions filters and pages ListSessions. Results are ordered newest first (CreatedAt desc, ID desc).
type ListOptions struct {
	Agent    string         // exact match when non-empty
	Metadata map[string]any // every key must equal the session's metadata value (scalar values)
	Limit    int            // <= 0 means no limit
	Offset   int
}

// Store persists agent state. Implementations must be safe for concurrent use.
//
// Semantics shared by all implementations (verified by storetest):
//   - CreateSession assigns ID (NewID) when empty and CreatedAt/UpdatedAt when zero.
//   - AppendMessages assigns ID, Seq and CreatedAt, bumps the session's UpdatedAt and returns
//     ErrNotFound for an unknown session.
//   - SaveRun upserts by ID (assigning one when empty); SaveToolCall inserts (assigning ID/CreatedAt).
//   - DeleteSession removes the session with its messages, runs and tool calls.
//   - RecoverInterrupted marks all running runs as interrupted (setting FinishedAt) and returns the count.
type Store interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	ListSessions(ctx context.Context, opts ListOptions) ([]Session, error)
	DeleteSession(ctx context.Context, id string) error
	AppendMessages(ctx context.Context, sessionID, runID string, msgs ...llm.Message) error
	History(ctx context.Context, sessionID string) ([]StoredMessage, error)
	SaveRun(ctx context.Context, r *Run) error
	GetRun(ctx context.Context, id string) (*Run, error)
	ListRuns(ctx context.Context, sessionID string) ([]Run, error) // oldest first
	SaveToolCall(ctx context.Context, rec *ToolCallRecord) error
	ListToolCalls(ctx context.Context, runID string) ([]ToolCallRecord, error) // insertion order
	RecoverInterrupted(ctx context.Context) (int, error)
}

// NewID returns a random UUIDv7 string (time ordered).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[:6], ts[2:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}
