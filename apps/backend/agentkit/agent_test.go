package agentkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

type numIn struct {
	N int `json:"n"`
}

func newAgent(t *testing.T, p llm.Provider, mod func(*agentkit.Config)) (*agentkit.Agent, *store.Session) {
	t.Helper()
	cfg := agentkit.Config{Name: "test", Provider: p, Model: "m", System: "be nice"}
	if mod != nil {
		mod(&cfg)
	}
	a, err := agentkit.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.NewSession(context.Background(), agentkit.SessionOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return a, sess
}

func collect(seq iter.Seq2[agentkit.Event, error]) ([]agentkit.Event, error) {
	var evs []agentkit.Event
	var last error
	for ev, err := range seq {
		evs = append(evs, ev)
		if err != nil {
			last = err
		}
	}
	return evs, last
}

func types(evs []agentkit.Event) []agentkit.EventType {
	var out []agentkit.EventType
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func history(t *testing.T, a *agentkit.Agent, id string) []llm.Message {
	t.Helper()
	h, err := a.History(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []llm.Message
	for _, sm := range h {
		out = append(out, sm.Message)
	}
	return out
}

func lastRun(t *testing.T, a *agentkit.Agent, sessionID string) store.Run {
	t.Helper()
	runs, err := a.Store().ListRuns(context.Background(), sessionID)
	if err != nil || len(runs) == 0 {
		t.Fatalf("ListRuns = %v, %v", runs, err)
	}
	return runs[len(runs)-1]
}

func toolResults(t *testing.T, msg llm.Message) []llm.Block {
	t.Helper()
	if msg.Role != llm.RoleTool {
		t.Fatalf("expected tool message, got %+v", msg)
	}
	return msg.BlocksOf(llm.BlockToolResult)
}

func TestNewValidation(t *testing.T) {
	if _, err := agentkit.New(agentkit.Config{}); err == nil {
		t.Fatal("missing provider accepted")
	}
	a, err := agentkit.New(agentkit.Config{Provider: fake.New()})
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	if cfg.MaxSteps != 20 || cfg.ToolTimeout != 2*time.Minute || cfg.Name != "agent" || cfg.Store == nil {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestPlainText(t *testing.T) {
	p := fake.New(fake.Turn{Message: llm.AssistantText("hello there world"), Stream: true, Usage: llm.Usage{InputTokens: 10, OutputTokens: 3}})
	var hooked []agentkit.EventType
	a, sess := newAgent(t, p, func(c *agentkit.Config) {
		c.Hooks.OnEvent = func(e agentkit.Event) { hooked = append(hooked, e.Type) }
	})
	evs, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	want := []agentkit.EventType{agentkit.EventRunStarted, agentkit.EventTextDelta, agentkit.EventTextDelta, agentkit.EventTextDelta,
		agentkit.EventStepFinished, agentkit.EventRunFinished}
	if got := types(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(hooked, want) {
		t.Fatalf("OnEvent saw %v", hooked)
	}
	var text strings.Builder
	for _, e := range evs {
		text.WriteString(e.Delta)
		if e.SessionID != sess.ID || e.RunID == "" {
			t.Fatalf("event missing ids: %+v", e)
		}
	}
	if text.String() != "hello there world" {
		t.Fatalf("deltas = %q", text.String())
	}
	fin := evs[len(evs)-1]
	if fin.Text != "hello there world" || fin.Usage.InputTokens != 10 || fin.Status != store.RunCompleted {
		t.Fatalf("run_finished = %+v", fin)
	}
	req := p.Requests()[0]
	if req.Model != "m" || req.System != "be nice" || len(req.Messages) != 1 || req.Messages[0].Text() != "hi" {
		t.Fatalf("request = %+v", req)
	}
	if h := history(t, a, sess.ID); len(h) != 2 || h[1].Role != llm.RoleAssistant {
		t.Fatalf("history = %+v", h)
	}
	run := lastRun(t, a, sess.ID)
	if run.Status != store.RunCompleted || run.Usage.OutputTokens != 3 || run.FinishedAt.IsZero() || run.Model != "m" {
		t.Fatalf("run = %+v", run)
	}
}

func TestParallelToolLoop(t *testing.T) {
	// Both calls must be in flight at once to pass the barrier.
	var started sync.WaitGroup
	started.Add(2)
	double := tool.New("double", "Doubles n", func(ctx context.Context, in numIn) (int, error) {
		started.Done()
		done := make(chan struct{})
		go func() { started.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			return 0, errors.New("calls were not parallel")
		}
		call, ok := agentkit.CallFromContext(ctx)
		if !ok || call.RunID == "" || call.Step != 1 {
			return 0, errors.New("missing call context")
		}
		return in.N * 2, nil
	})
	p := fake.New(
		fake.Turn{
			Message: llm.NewMessage(llm.RoleAssistant,
				llm.Block{Type: llm.BlockText, Text: "computing", Phase: llm.PhaseCommentary},
				fake.Call("c1", "double", numIn{2}), fake.Call("c2", "double", numIn{5})),
			Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		fake.Turn{Message: llm.AssistantText("4 and 10"), Usage: llm.Usage{InputTokens: 20, OutputTokens: 2, CachedInputTokens: 8}},
	)
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(double) })
	evs, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("double 2 and 5")))
	if err != nil {
		t.Fatal(err)
	}
	want := []agentkit.EventType{agentkit.EventRunStarted, agentkit.EventStepFinished,
		agentkit.EventToolCallStarted, agentkit.EventToolCallStarted, agentkit.EventToolCallFinished, agentkit.EventToolCallFinished,
		agentkit.EventStepFinished, agentkit.EventRunFinished}
	if got := types(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	reqs := p.Requests()
	if len(reqs) != 2 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "double" {
		t.Fatalf("requests = %+v", reqs)
	}
	second := reqs[1].Messages
	if len(second) != 3 || second[1].Role != llm.RoleAssistant || len(second[1].ToolCalls()) != 2 {
		t.Fatalf("second request messages = %+v", second)
	}
	res := toolResults(t, second[2])
	if len(res) != 2 || res[0].ToolCallID != "c1" || res[0].Output != "4" || res[1].ToolCallID != "c2" || res[1].Output != "10" || res[0].IsError {
		t.Fatalf("tool results = %+v", res)
	}
	fin := evs[len(evs)-1]
	if fin.Text != "4 and 10" || *fin.Usage != (llm.Usage{InputTokens: 30, OutputTokens: 7, CachedInputTokens: 8}) {
		t.Fatalf("run_finished = %+v", fin)
	}
	run := lastRun(t, a, sess.ID)
	recs, _ := a.Store().ListToolCalls(context.Background(), run.ID)
	if len(recs) != 2 || run.Usage.InputTokens != 30 {
		t.Fatalf("records = %+v, run = %+v", recs, run)
	}
	if h := history(t, a, sess.ID); len(h) != 4 {
		t.Fatalf("history len = %d", len(h))
	}
}

// runToolTurn runs one tool-calling turn followed by a text answer and returns the tool results
// sent back to the model.
func runToolTurn(t *testing.T, cfg func(*agentkit.Config), calls ...llm.Block) ([]llm.Block, []agentkit.Event) {
	t.Helper()
	p := fake.New(fake.ToolCalls(calls...), fake.Text("done"))
	a, sess := newAgent(t, p, cfg)
	evs, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("go")))
	if err != nil {
		t.Fatal(err)
	}
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	msgs := reqs[1].Messages
	return toolResults(t, msgs[len(msgs)-1]), evs
}

func TestToolErrors(t *testing.T) {
	fail := tool.New("fail", "", func(context.Context, struct{}) (string, error) { return "", errors.New("disk on fire") })
	boom := tool.New("boom", "", func(context.Context, struct{}) (string, error) { panic("kaboom") })
	slow := tool.New("slow", "", func(context.Context, struct{}) (string, error) {
		time.Sleep(time.Second) // ignores ctx
		return "late", nil
	})
	ok := tool.New("ok", "", func(context.Context, numIn) (string, error) { return "fine", nil })
	res, evs := runToolTurn(t, func(c *agentkit.Config) {
		c.Tools = tool.NewSet(fail, boom, slow, ok)
		c.ToolTimeout = 50 * time.Millisecond
	},
		fake.Call("1", "fail", struct{}{}),
		fake.Call("2", "boom", struct{}{}),
		fake.Call("3", "slow", struct{}{}),
		fake.Call("4", "missing", struct{}{}),
		fake.Call("5", "ok", map[string]any{"bogus": 1}),
		fake.Call("6", "ok", numIn{1}),
	)
	want := []struct {
		isErr bool
		sub   string
	}{
		{true, "disk on fire"}, {true, "panicked: kaboom"}, {true, "timed out"},
		{true, `unknown tool "missing"`}, {true, "unknown field"}, {false, "fine"},
	}
	if len(res) != len(want) {
		t.Fatalf("results = %+v", res)
	}
	for i, w := range want {
		if res[i].IsError != w.isErr || !strings.Contains(res[i].Output, w.sub) || res[i].ToolCallID != string(rune('1'+i)) {
			t.Errorf("result %d = %+v, want isErr=%v containing %q", i, res[i], w.isErr, w.sub)
		}
	}
	finished := 0
	for _, e := range evs {
		if e.Type == agentkit.EventToolCallFinished {
			finished++
		}
	}
	if finished != 6 {
		t.Fatalf("tool_call_finished events = %d", finished)
	}
}

func TestVetoedTool(t *testing.T) {
	var called []string
	var mu sync.Mutex
	echo := tool.New("echo", "", func(_ context.Context, in numIn) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		called = append(called, "echo")
		return in.N, nil
	})
	res, _ := runToolTurn(t, func(c *agentkit.Config) {
		c.Tools = tool.NewSet(echo)
		c.Hooks.BeforeToolCall = func(_ context.Context, call *agentkit.ToolCall) error {
			if call.ID == "deny" {
				return errors.New("not allowed")
			}
			call.Input = json.RawMessage(`{"n":42}`) // hooks may rewrite input
			return nil
		}
	}, fake.Call("deny", "echo", numIn{1}), fake.Call("allow", "echo", numIn{2}))
	if !res[0].IsError || !strings.Contains(res[0].Output, "not allowed") {
		t.Fatalf("vetoed result = %+v", res[0])
	}
	if res[1].IsError || res[1].Output != "42" {
		t.Fatalf("allowed result = %+v", res[1])
	}
	if len(called) != 1 {
		t.Fatalf("tool called %d times", len(called))
	}
}

func TestMaxSteps(t *testing.T) {
	echo := tool.New("echo", "", func(_ context.Context, in numIn) (int, error) { return in.N, nil })
	p := fake.New(
		fake.ToolCalls(fake.Call("a", "echo", numIn{1})),
		fake.ToolCalls(fake.Call("b", "echo", numIn{2})),
		fake.Text("never reached"),
	)
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(echo); c.MaxSteps = 2 })
	evs, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("loop")))
	if !errors.Is(err, agentkit.ErrMaxSteps) {
		t.Fatalf("err = %v", err)
	}
	last := evs[len(evs)-1]
	if last.Type != agentkit.EventRunFailed || last.Status != store.RunFailed {
		t.Fatalf("last event = %+v", last)
	}
	if p.Remaining() != 1 {
		t.Fatalf("provider called %d times", 3-p.Remaining())
	}
	if run := lastRun(t, a, sess.ID); run.Status != store.RunFailed || !strings.Contains(run.Error, "max steps") {
		t.Fatalf("run = %+v", run)
	}
}

func TestProviderError(t *testing.T) {
	p := fake.New(fake.Turn{Err: errors.New("rate limited")})
	a, sess := newAgent(t, p, nil)
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("x"))
	if err == nil || !strings.Contains(err.Error(), "rate limited") || res.Status != store.RunFailed {
		t.Fatalf("RunSync = %+v, %v", res, err)
	}
	if run := lastRun(t, a, sess.ID); run.Status != store.RunFailed {
		t.Fatalf("run = %+v", run)
	}
}

func TestCancellation(t *testing.T) {
	blocked := make(chan struct{})
	wait := tool.New("wait", "", func(ctx context.Context, _ struct{}) (string, error) {
		close(blocked)
		<-ctx.Done()
		return "", ctx.Err()
	})
	t.Run("context", func(t *testing.T) {
		p := fake.New(fake.ToolCalls(fake.Call("w", "wait", struct{}{})), fake.Text("unused"))
		a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(wait) })
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-blocked; cancel() }()
		evs, err := collect(a.Run(ctx, sess.ID, agentkit.UserText("x")))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if last := evs[len(evs)-1]; last.Type != agentkit.EventRunFailed || last.Status != store.RunCancelled {
			t.Fatalf("last event = %+v", last)
		}
		if run := lastRun(t, a, sess.ID); run.Status != store.RunCancelled || run.FinishedAt.IsZero() {
			t.Fatalf("run = %+v", run)
		}
		// The tool result is still persisted so call/result pairs stay intact.
		h := history(t, a, sess.ID)
		if len(h) != 3 || !toolResults(t, h[2])[0].IsError {
			t.Fatalf("history = %+v", h)
		}
		if a.Busy(sess.ID) {
			t.Fatal("session still busy")
		}
	})
	t.Run("Agent.Cancel", func(t *testing.T) {
		p := fake.New(fake.Turn{Message: llm.AssistantText("partial answer"), Stream: true, Hang: true})
		a, sess := newAgent(t, p, nil)
		var err error
		for ev, e := range a.Run(context.Background(), sess.ID, agentkit.UserText("x")) {
			if ev.Type == agentkit.EventTextDelta && !a.Cancel(sess.ID) {
				t.Error("Cancel found no active run")
			}
			err = e
		}
		if !errors.Is(err, context.Canceled) || lastRun(t, a, sess.ID).Status != store.RunCancelled {
			t.Fatalf("err = %v, run = %+v", err, lastRun(t, a, sess.ID))
		}
		if h := history(t, a, sess.ID); len(h) != 1 {
			t.Fatalf("partial assistant message persisted: %+v", h)
		}
	})
	t.Run("consumer break", func(t *testing.T) {
		p := fake.New(fake.Turn{Message: llm.AssistantText("one two three"), Stream: true})
		var last agentkit.Event
		a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Hooks.OnEvent = func(e agentkit.Event) { last = e } })
		for ev := range a.Run(context.Background(), sess.ID, agentkit.UserText("x")) {
			if ev.Type == agentkit.EventTextDelta {
				break
			}
		}
		if run := lastRun(t, a, sess.ID); run.Status != store.RunCancelled {
			t.Fatalf("run = %+v", run)
		}
		if last.Type != agentkit.EventRunFailed {
			t.Fatalf("OnEvent last = %+v", last)
		}
	})
}

func TestBusySession(t *testing.T) {
	started := make(chan struct{})
	p := fake.New(fake.Turn{Message: llm.AssistantText("slow"), Hang: true, Check: func(*llm.Request) error { close(started); return nil }})
	a, sess := newAgent(t, p, nil)
	done := make(chan error, 1)
	go func() {
		_, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("1"))
		done <- err
	}()
	<-started
	_, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("2")))
	if !errors.Is(err, agentkit.ErrSessionBusy) {
		t.Fatalf("second run err = %v", err)
	}
	if err := a.DeleteSession(context.Background(), sess.ID); !errors.Is(err, agentkit.ErrSessionBusy) {
		t.Fatalf("DeleteSession err = %v", err)
	}
	// Other sessions are unaffected.
	other, _ := a.NewSession(context.Background(), agentkit.SessionOptions{})
	p.Push(fake.Text("ok"))
	if res, err := a.RunSync(context.Background(), other.ID, agentkit.UserText("x")); err != nil || res.Text != "ok" {
		t.Fatalf("other session = %+v, %v", res, err)
	}
	a.Cancel(sess.ID)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first run err = %v", err)
	}
	if err := a.DeleteSession(context.Background(), sess.ID); err != nil {
		t.Fatalf("DeleteSession after run: %v", err)
	}
}

func TestUnknownSession(t *testing.T) {
	a, _ := newAgent(t, fake.New(), nil)
	if _, err := a.RunSync(context.Background(), "nope", agentkit.UserText("x")); !errors.Is(err, agentkit.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// Sessions of other agents sharing the store are invisible.
	other, _ := agentkit.New(agentkit.Config{Name: "other", Provider: fake.New(), Store: a.Store()})
	s, _ := other.NewSession(context.Background(), agentkit.SessionOptions{})
	if _, err := a.GetSession(context.Background(), s.ID); !errors.Is(err, agentkit.ErrNotFound) {
		t.Fatalf("GetSession(other agent) err = %v", err)
	}
	if list, _ := a.ListSessions(context.Background(), store.ListOptions{}); len(list) != 1 {
		t.Fatalf("ListSessions = %+v", list)
	}
}

func TestAsTool(t *testing.T) {
	subProvider := fake.New(fake.Turn{
		Message: llm.AssistantText("it is too long"),
		Check: func(r *llm.Request) error {
			if r.Messages[0].Text() != "critique: hello" {
				return errors.New("unexpected prompt " + r.Messages[0].Text())
			}
			return nil
		},
	})
	sub, err := agentkit.New(agentkit.Config{Name: "critic", Provider: subProvider})
	if err != nil {
		t.Fatal(err)
	}
	critic := agentkit.AsTool(sub, "critic", "Critiques a text")
	if !strings.Contains(string(critic.Spec().Schema), `"prompt"`) {
		t.Fatalf("schema = %s", critic.Spec().Schema)
	}
	res, _ := runToolTurn(t, func(c *agentkit.Config) { c.Tools = tool.NewSet(critic) },
		fake.Call("c1", "critic", map[string]string{"prompt": "critique: hello"}))
	if res[0].IsError || res[0].Output != "it is too long" {
		t.Fatalf("result = %+v", res[0])
	}
	sessions, _ := sub.ListSessions(context.Background(), store.ListOptions{})
	if len(sessions) != 1 || sessions[0].Metadata["parent_tool_call_id"] != "c1" || sessions[0].Metadata["parent_session_id"] == "" {
		t.Fatalf("sub sessions = %+v", sessions)
	}
}

func TestHistoryPersistsAcrossRuns(t *testing.T) {
	opaque := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"ZW5j","summary":[{"type":"summary_text","text":"thinking"}]}`)
	first := llm.NewMessage(llm.RoleAssistant, llm.ReasoningBlock("fake", opaque, "thinking"), llm.TextBlock("first answer"))
	p := fake.New(
		fake.Turn{Message: first, Stream: true},
		fake.Text("second answer"),
	)
	a, sess := newAgent(t, p, nil)
	evs, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("q1")))
	if err != nil {
		t.Fatal(err)
	}
	if evs[1].Type != agentkit.EventReasoningDelta {
		t.Fatalf("expected reasoning delta, got %v", types(evs))
	}
	if _, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("q2")); err != nil {
		t.Fatal(err)
	}
	msgs := p.Requests()[1].Messages
	want := []llm.Message{agentkit.UserText("q1"), first, agentkit.UserText("q2")}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("second request =\n%+v\nwant\n%+v", msgs, want)
	}
	if string(msgs[1].Blocks[0].Opaque) != string(opaque) || msgs[1].Blocks[0].Provider != "fake" {
		t.Fatalf("opaque not replayed verbatim: %s", msgs[1].Blocks[0].Opaque)
	}
	h, _ := a.History(context.Background(), sess.ID)
	if len(h) != 4 || h[0].RunID == h[2].RunID || h[0].RunID != h[1].RunID {
		t.Fatalf("history run ids = %+v", h)
	}
}

func TestContextPolicyAndBeforeRequest(t *testing.T) {
	reasoning := llm.ReasoningBlock("fake", json.RawMessage(`{"big":"`+strings.Repeat("x", 100)+`"}`), "")
	p := fake.New(
		fake.Turn{Message: llm.NewMessage(llm.RoleAssistant, reasoning, llm.TextBlock("a1"))},
		fake.Turn{Message: llm.NewMessage(llm.RoleAssistant, reasoning)},
		fake.Text("a2"),
	)
	a, sess := newAgent(t, p, func(c *agentkit.Config) {
		c.ContextPolicy = agentkit.DropSettledReasoning(50)
		c.Hooks.BeforeRequest = func(_ context.Context, s *store.Session, r *llm.Request) error {
			r.SafetyID = "user-" + s.Title
			return nil
		}
	})
	if _, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("q1")); err != nil {
		t.Fatal(err)
	}
	// Second turn produces a reasoning-only message (no tool calls, so the run ends with empty text).
	if _, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("q2")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("q3")); err != nil {
		t.Fatal(err)
	}
	reqs := p.Requests()
	if reqs[0].SafetyID != "user-t" {
		t.Fatalf("SafetyID = %q", reqs[0].SafetyID)
	}
	msgs := reqs[2].Messages
	want := []llm.Message{agentkit.UserText("q1"), llm.AssistantText("a1"), agentkit.UserText("q2"), agentkit.UserText("q3")}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("policy output = %+v", msgs)
	}
	if h := history(t, a, sess.ID); len(h[1].Blocks) != 2 {
		t.Fatal("policy mutated stored history")
	}
}

func TestRecover(t *testing.T) {
	a, sess := newAgent(t, fake.New(), nil)
	ctx := context.Background()
	if err := a.Store().SaveRun(ctx, &store.Run{SessionID: sess.ID, Agent: "test", Status: store.RunRunning}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Recover(ctx); err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	if run := lastRun(t, a, sess.ID); run.Status != store.RunInterrupted {
		t.Fatalf("run = %+v", run)
	}
}
