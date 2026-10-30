// Package memory is a thread-safe in-memory store.Store.
package memory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
)

// Store keeps everything in process memory.
type Store struct {
	mu        sync.RWMutex
	sessions  map[string]*store.Session
	messages  map[string][]store.StoredMessage // by session
	runs      map[string]*store.Run
	toolCalls []store.ToolCallRecord
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{
		sessions: map[string]*store.Session{},
		messages: map[string][]store.StoredMessage{},
		runs:     map[string]*store.Run{},
	}
}

func copySession(s *store.Session) *store.Session {
	c := *s
	c.Metadata = maps.Clone(s.Metadata)
	return &c
}

func (m *Store) CreateSession(_ context.Context, s *store.Session) error {
	if s.ID == "" {
		s.ID = store.NewID()
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = s.CreatedAt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = copySession(s)
	return nil
}

func (m *Store) GetSession(_ context.Context, id string) (*store.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copySession(s), nil
}

func (m *Store) ListSessions(_ context.Context, opts store.ListOptions) ([]store.Session, error) {
	m.mu.RLock()
	var out []store.Session
	for _, s := range m.sessions {
		if opts.Agent != "" && s.Agent != opts.Agent {
			continue
		}
		if !metadataMatches(s.Metadata, opts.Metadata) {
			continue
		}
		out = append(out, *copySession(s))
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b store.Session) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	out = out[min(max(opts.Offset, 0), len(out)):]
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func metadataMatches(have, want map[string]any) bool {
	for k, w := range want {
		h, ok := have[k]
		if !ok {
			return false
		}
		hb, err1 := json.Marshal(h)
		wb, err2 := json.Marshal(w)
		if err1 != nil || err2 != nil || !bytes.Equal(hb, wb) {
			return false
		}
	}
	return true
}

func (m *Store) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.sessions, id)
	delete(m.messages, id)
	maps.DeleteFunc(m.runs, func(_ string, r *store.Run) bool { return r.SessionID == id })
	m.toolCalls = slices.DeleteFunc(m.toolCalls, func(r store.ToolCallRecord) bool { return r.SessionID == id })
	return nil
}

func (m *Store) AppendMessages(_ context.Context, sessionID, runID string, msgs ...llm.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	if len(msgs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	hist := m.messages[sessionID]
	seq := int64(len(hist))
	for _, msg := range msgs {
		seq++
		hist = append(hist, store.StoredMessage{
			ID: store.NewID(), SessionID: sessionID, RunID: runID, Seq: seq, Message: msg.Clone(), CreatedAt: now,
		})
	}
	m.messages[sessionID] = hist
	s.UpdatedAt = now
	return nil
}

func (m *Store) History(_ context.Context, sessionID string) ([]store.StoredMessage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.sessions[sessionID]; !ok {
		return nil, store.ErrNotFound
	}
	hist := m.messages[sessionID]
	out := make([]store.StoredMessage, len(hist))
	for i, sm := range hist {
		sm.Message = sm.Message.Clone()
		out[i] = sm
	}
	return out, nil
}

func (m *Store) SaveRun(_ context.Context, r *store.Run) error {
	if r.ID == "" {
		r.ID = store.NewID()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *r
	m.runs[r.ID] = &c
	return nil
}

func (m *Store) GetRun(_ context.Context, id string) (*store.Run, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (m *Store) ListRuns(_ context.Context, sessionID string) ([]store.Run, error) {
	m.mu.RLock()
	var out []store.Run
	for _, r := range m.runs {
		if r.SessionID == sessionID {
			out = append(out, *r)
		}
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b store.Run) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (m *Store) SaveToolCall(_ context.Context, rec *store.ToolCallRecord) error {
	if rec.ID == "" {
		rec.ID = store.NewID()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *rec
	c.Input = slices.Clone(rec.Input)
	m.toolCalls = append(m.toolCalls, c)
	return nil
}

func (m *Store) ListToolCalls(_ context.Context, runID string) ([]store.ToolCallRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []store.ToolCallRecord
	for _, r := range m.toolCalls {
		if r.RunID == runID {
			r.Input = slices.Clone(r.Input)
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *Store) RecoverInterrupted(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	n := 0
	for _, r := range m.runs {
		if r.Status == store.RunRunning {
			r.Status = store.RunInterrupted
			r.FinishedAt = now
			n++
		}
	}
	return n, nil
}
