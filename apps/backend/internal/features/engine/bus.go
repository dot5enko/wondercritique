package engine

import (
	"encoding/json"
	"sync"
	"time"
)

// busEvent is one run event as framed on the SSE stream: Name is the SSE event
// name, Data the JSON payload (which repeats the name under "event", the shape
// the frontend's mapRunEvent reads).
type busEvent struct {
	Name string
	Data []byte
}

// Terminal events end a run's stream.
func (e busEvent) terminal() bool { return e.Name == "run_completed" || e.Name == "run_failed" }

const (
	subBuffer    = 256
	historyLimit = 4000
	busRetention = 5 * time.Minute
)

// eventBus fans run events out to SSE subscribers and keeps a bounded replay
// history so late subscribers (the client connects after POST /runs returned)
// see everything from the start.
type eventBus struct {
	mu   sync.Mutex
	runs map[string]*runBus
}

type runBus struct {
	history []busEvent
	subs    map[*subscriber]struct{}
	closed  bool
}

type subscriber struct{ ch chan busEvent }

func newEventBus() *eventBus { return &eventBus{runs: map[string]*runBus{}} }

// open registers a run so subscribers arriving before the first event find it.
func (b *eventBus) open(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.runs[runID]; !ok {
		b.runs[runID] = &runBus{subs: map[*subscriber]struct{}{}}
	}
}

// publish appends an event and delivers it to every subscriber. A subscriber
// that cannot keep up is dropped (its channel closes; the client reconnects
// and replays).
func (b *eventBus) publish(runID, name string, fields map[string]any) {
	payload := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		payload[k] = v
	}
	payload["event"] = name
	payload["run_id"] = runID
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := busEvent{Name: name, Data: data}

	b.mu.Lock()
	defer b.mu.Unlock()
	rb, ok := b.runs[runID]
	if !ok || rb.closed {
		return
	}
	if len(rb.history) >= historyLimit {
		rb.history = append(rb.history[:0], rb.history[len(rb.history)/2:]...)
	}
	rb.history = append(rb.history, ev)
	for s := range rb.subs {
		select {
		case s.ch <- ev:
		default:
			close(s.ch)
			delete(rb.subs, s)
		}
	}
}

// closeRun ends the stream of a run: subscribers see their channel close after
// the terminal event; the history stays available for a while.
func (b *eventBus) closeRun(runID string) {
	b.mu.Lock()
	rb, ok := b.runs[runID]
	if !ok || rb.closed {
		b.mu.Unlock()
		return
	}
	rb.closed = true
	for s := range rb.subs {
		close(s.ch)
	}
	rb.subs = map[*subscriber]struct{}{}
	b.mu.Unlock()
	time.AfterFunc(busRetention, func() {
		b.mu.Lock()
		delete(b.runs, runID)
		b.mu.Unlock()
	})
}

// subscribe returns the history so far and, while the run is live, a channel
// of subsequent events (nil once the run's stream is closed). ok is false when
// the bus knows nothing about the run.
func (b *eventBus) subscribe(runID string) (replay []busEvent, sub *subscriber, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rb, found := b.runs[runID]
	if !found {
		return nil, nil, false
	}
	replay = append([]busEvent(nil), rb.history...)
	if rb.closed {
		return replay, nil, true
	}
	sub = &subscriber{ch: make(chan busEvent, subBuffer)}
	rb.subs[sub] = struct{}{}
	return replay, sub, true
}

func (b *eventBus) unsubscribe(runID string, sub *subscriber) {
	if sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if rb, ok := b.runs[runID]; ok {
		if _, live := rb.subs[sub]; live {
			delete(rb.subs, sub)
			close(sub.ch)
		}
	}
}
