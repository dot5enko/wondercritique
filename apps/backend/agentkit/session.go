package agentkit

import (
	"context"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
)

// SessionOptions configures NewSession.
type SessionOptions struct {
	ID       string // optional; generated when empty
	Title    string
	Metadata map[string]any
}

// NewSession creates a session owned by the agent.
func (a *Agent) NewSession(ctx context.Context, opts SessionOptions) (*store.Session, error) {
	s := &store.Session{ID: opts.ID, Agent: a.cfg.Name, Title: opts.Title, Metadata: opts.Metadata}
	if err := a.cfg.Store.CreateSession(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// GetSession returns a session owned by the agent, or ErrNotFound.
func (a *Agent) GetSession(ctx context.Context, id string) (*store.Session, error) {
	s, err := a.cfg.Store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Agent != a.cfg.Name {
		return nil, ErrNotFound
	}
	return s, nil
}

// ListSessions lists the agent's sessions (opts.Agent is forced to the agent name).
func (a *Agent) ListSessions(ctx context.Context, opts store.ListOptions) ([]store.Session, error) {
	opts.Agent = a.cfg.Name
	return a.cfg.Store.ListSessions(ctx, opts)
}

// DeleteSession deletes a session and its history. It fails with ErrSessionBusy during a run.
func (a *Agent) DeleteSession(ctx context.Context, id string) error {
	if _, err := a.GetSession(ctx, id); err != nil {
		return err
	}
	if !a.acquire(id, func() {}) {
		return ErrSessionBusy
	}
	defer a.release(id)
	return a.cfg.Store.DeleteSession(ctx, id)
}

// History returns the persisted messages of a session in order.
func (a *Agent) History(ctx context.Context, sessionID string) ([]store.StoredMessage, error) {
	if _, err := a.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	return a.cfg.Store.History(ctx, sessionID)
}

// UserText returns a user message with a single text block.
func UserText(text string) llm.Message { return llm.UserText(text) }
