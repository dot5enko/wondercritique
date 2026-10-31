// Package httpapi exposes an agent over HTTP: session management and runs,
// streamed as server-sent events. The returned handler is mountable under any
// path prefix (it uses relative patterns of the Go 1.22+ ServeMux).
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
)

// Options configures the handler. SessionOwner, when set, scopes every session
// to the value it derives from the request: sessions are created with
// metadata["owner"], lists are filtered, and access to other owners' sessions
// returns 404. An error from SessionOwner yields 401.
type Options struct {
	SessionOwner func(*http.Request) (string, error)
	Logger       *slog.Logger
	Heartbeat    time.Duration // SSE keepalive comment interval, default 15s
}

type handler struct {
	agent *agentkit.Agent
	opts  Options
}

// New builds the API handler for one agent.
func New(a *agentkit.Agent, opts Options) http.Handler {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 15 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	h := &handler{agent: a, opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", h.createSession)
	mux.HandleFunc("GET /sessions", h.listSessions)
	mux.HandleFunc("GET /sessions/{id}", h.getSession)
	mux.HandleFunc("DELETE /sessions/{id}", h.deleteSession)
	mux.HandleFunc("POST /sessions/{id}/runs", h.startRun)
	mux.HandleFunc("POST /sessions/{id}/cancel", h.cancelRun)
	return mux
}

// --- helpers --------------------------------------------------------------

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": msg},
	})
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "session not found")
	case errors.Is(err, agentkit.ErrSessionBusy):
		writeErr(w, http.StatusConflict, "busy", "a run is already active in this session")
	case errors.Is(err, agentkit.ErrMaxSteps):
		writeErr(w, http.StatusConflict, "max_steps", "run exceeded the step limit")
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return v, false
	}
	return v, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// owner resolves the session owner requirement, returning a sentinel for responses.
func (h *handler) owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.opts.SessionOwner == nil {
		return "", true
	}
	o, err := h.opts.SessionOwner(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return "", false
	}
	return o, true
}

// owns reports whether the session belongs to the requesting owner.
func owns(sess *store.Session, owner string) bool {
	if sess == nil {
		return false
	}
	if o, ok := sess.Metadata["owner"]; ok && owner != "" {
		return o == owner
	}
	return owner == "" // sessions without an owner are public
}

// --- sessions --------------------------------------------------------------

type createSessionRequest struct {
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[createSessionRequest](w, r)
	if !ok {
		return
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	if owner != "" {
		req.Metadata["owner"] = owner
	}
	sess, err := h.agent.NewSession(r.Context(), agentkit.SessionOptions{Title: req.Title, Metadata: req.Metadata})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	sessions, err := h.agent.ListSessions(r.Context(), store.ListOptions{
		Metadata: ifOwner(owner), Limit: limit, Offset: offset,
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// ifOwner returns a metadata filter for the owner scope.
func ifOwner(owner string) map[string]any {
	if owner == "" {
		return nil
	}
	return map[string]any{"owner": owner}
}

// scoped fetches the session and enforces ownership.
func (h *handler) scoped(w http.ResponseWriter, r *http.Request, owner string) (*store.Session, bool) {
	sess, err := h.agent.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return nil, false
	}
	if !owns(sess, owner) {
		writeErr(w, http.StatusNotFound, "not_found", "session not found")
		return nil, false
	}
	return sess, true
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	if _, ok := h.scoped(w, r, owner); !ok {
		return
	}
	history, err := h.agent.History(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": history})
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	if _, ok := h.scoped(w, r, owner); !ok {
		return
	}
	if err := h.agent.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- runs ------------------------------------------------------------------

type startRunRequest struct {
	Input    string        `json:"input"`    // plain user text
	Messages []llm.Message `json:"messages"` // or full messages
}

func (h *handler) runMessages(req startRunRequest) []llm.Message {
	if len(req.Messages) > 0 {
		return req.Messages
	}
	if req.Input != "" {
		return []llm.Message{llm.UserText(req.Input)}
	}
	return nil
}

type runResponse struct {
	RunID    string        `json:"run_id"`
	Status   string        `json:"status"`
	Text     string        `json:"text"`
	Usage    llm.Usage     `json:"usage"`
	Steps    int           `json:"steps"`
	Messages []llm.Message `json:"messages,omitempty"`
}

func (h *handler) startRun(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	if _, ok := h.scoped(w, r, owner); !ok {
		return
	}
	req, ok := decodeJSON[startRunRequest](w, r)
	if !ok {
		return
	}
	msgs := h.runMessages(req)
	if len(msgs) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", `provide "input" or "messages"`)
		return
	}
	if h.agent.Busy(r.PathValue("id")) {
		writeErr(w, http.StatusConflict, "busy", "a run is already active in this session")
		return
	}
	stream := r.URL.Query().Get("stream") != "false"
	if !stream {
		res, err := h.agent.RunSync(r.Context(), r.PathValue("id"), msgs...)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, runResponse{
			RunID: res.RunID, Status: string(res.Status), Text: res.Text,
			Usage: res.Usage, Steps: res.Steps, Messages: res.Messages,
		})
		return
	}
	h.streamRun(w, r, r.PathValue("id"), msgs)
}

// streamRun executes a run and writes each event as an SSE frame. The client
// disconnecting cancels the run.
func (h *handler) streamRun(w http.ResponseWriter, r *http.Request, sessionID string, msgs []llm.Message) {
	ctx := r.Context()
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	events := make(chan agentkit.Event, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		for ev, err := range h.agent.Run(ctx, sessionID, msgs...) {
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return // the run_failed event was delivered above
			}
		}
	}()

	hb := time.NewTicker(h.opts.Heartbeat)
	defer hb.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				break loop
			}
			fl.Flush()
		case ev, ok := <-events:
			if !ok {
				break loop
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				break loop
			}
			fl.Flush()
		}
	}
	<-done // let the run finish persisting state
	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	fl.Flush()
}

func (h *handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	if _, ok := h.scoped(w, r, owner); !ok {
		return
	}
	if !h.agent.Cancel(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "no_active_run", "no active run in this session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
