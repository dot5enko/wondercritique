package agentkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

// A terminal tool's output string is the run's final answer: exactly one model step happens,
// the scripted follow-up turn is never consumed, and the tool result is persisted as usual.
func TestTerminalToolEndsRun(t *testing.T) {
	submit := tool.AsTerminal(tool.New("submit", "Ends the run", func(_ context.Context, in numIn) (string, error) {
		return "final answer 7", nil
	}))
	p := fake.New(
		fake.Turn{
			Message: llm.NewMessage(llm.RoleAssistant, llm.TextBlock("submitting"), fake.Call("t1", "submit", numIn{7})),
			Usage:   llm.Usage{InputTokens: 10, OutputTokens: 4},
		},
		fake.Text("this turn must never be reached"),
	)
	var hooked []agentkit.Event
	a, sess := newAgent(t, p, func(c *agentkit.Config) {
		c.Tools = tool.NewSet(submit)
		c.Hooks.OnEvent = func(e agentkit.Event) { hooked = append(hooked, e) }
	})
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.RunCompleted || res.Text != "final answer 7" || res.Steps != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage != (llm.Usage{InputTokens: 10, OutputTokens: 4}) {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(res.Messages) != 2 || len(res.Messages[0].ToolCalls()) != 1 {
		t.Fatalf("messages = %+v", res.Messages)
	}
	want := []agentkit.EventType{agentkit.EventRunStarted, agentkit.EventStepFinished,
		agentkit.EventToolCallStarted, agentkit.EventToolCallFinished, agentkit.EventRunFinished}
	if got := types(hooked); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	fin := hooked[len(hooked)-1]
	if fin.Text != "final answer 7" || fin.Status != store.RunCompleted || fin.Usage.InputTokens != 10 {
		t.Fatalf("run_finished = %+v", fin)
	}
	if reqs := p.Requests(); len(reqs) != 1 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "submit" {
		t.Fatalf("requests = %+v", reqs)
	}
	if p.Remaining() != 1 {
		t.Fatal("provider consumed the follow-up turn")
	}
	h := history(t, a, sess.ID)
	if len(h) != 3 || len(h[1].ToolCalls()) != 1 {
		t.Fatalf("history = %+v", h)
	}
	results := toolResults(t, h[2])
	if len(results) != 1 || results[0].Output != "final answer 7" || results[0].IsError {
		t.Fatalf("tool results = %+v", results)
	}
	run := lastRun(t, a, sess.ID)
	if run.Status != store.RunCompleted || run.Usage.OutputTokens != 4 || run.FinishedAt.IsZero() {
		t.Fatalf("run = %+v", run)
	}
	recs, err := a.Store().ListToolCalls(context.Background(), run.ID)
	if err != nil || len(recs) != 1 || recs[0].Output != "final answer 7" || recs[0].IsError {
		t.Fatalf("tool call records = %+v, %v", recs, err)
	}
}

// A tool may also implement tool.Terminal directly instead of being wrapped.
func TestTerminalToolDirect(t *testing.T) {
	p := fake.New(fake.ToolCalls(fake.Call("d1", "done", struct{}{})), fake.Text("unreachable"))
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(doneTool{}) })
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
	if err != nil || res.Status != store.RunCompleted || res.Text != "done output" || res.Steps != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if len(p.Requests()) != 1 {
		t.Fatalf("requests = %d", len(p.Requests()))
	}
}

type doneTool struct{}

func (doneTool) Spec() llm.ToolSpec                                    { return llm.ToolSpec{Name: "done"} }
func (doneTool) Call(context.Context, json.RawMessage) (string, error) { return "done output", nil }
func (doneTool) IsTerminal() bool                                      { return true }

// A non-terminal tool must not end the run: its result goes back to the model, which answers
// with text in a second step.
func TestNonTerminalToolLoops(t *testing.T) {
	echo := tool.New("echo", "Echoes n", func(_ context.Context, in numIn) (int, error) { return in.N, nil })
	p := fake.New(
		fake.ToolCalls(fake.Call("e1", "echo", numIn{3})),
		fake.Turn{Message: llm.AssistantText("it is 3"), Usage: llm.Usage{InputTokens: 5, OutputTokens: 1}},
	)
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(echo) })
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.RunCompleted || res.Text != "it is 3" || res.Steps != 2 {
		t.Fatalf("result = %+v", res)
	}
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	msgs := reqs[1].Messages
	if len(msgs) != 3 || msgs[1].Role != llm.RoleAssistant || len(msgs[1].ToolCalls()) != 1 {
		t.Fatalf("second request = %+v", msgs)
	}
	results := toolResults(t, msgs[2])
	if len(results) != 1 || results[0].Output != "3" || results[0].IsError {
		t.Fatalf("tool results = %+v", results)
	}
}

// A failed terminal call still ends the run: the error text is what the run records as the
// tool result output, so that text becomes the final answer and the run completes (it never
// fails and no further model call happens).
func TestTerminalToolError(t *testing.T) {
	submit := tool.AsTerminal(tool.New("submit", "", func(context.Context, struct{}) (string, error) {
		return "", errors.New("schema validation failed")
	}))
	p := fake.New(fake.ToolCalls(fake.Call("s1", "submit", struct{}{})), fake.Text("unreachable"))
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(submit) })
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
	if err != nil {
		t.Fatalf("failed terminal call must still complete the run, got %v", err)
	}
	if res.Status != store.RunCompleted || res.Text != "schema validation failed" || res.Steps != 1 {
		t.Fatalf("result = %+v", res)
	}
	h := history(t, a, sess.ID)
	results := toolResults(t, h[2])
	if len(results) != 1 || !results[0].IsError || results[0].Output != "schema validation failed" {
		t.Fatalf("tool results = %+v", results)
	}
	if run := lastRun(t, a, sess.ID); run.Status != store.RunCompleted {
		t.Fatalf("run = %+v", run)
	}
	if reqs := p.Requests(); len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
}

// A vetoed terminal call never reached the tool, so it does not end the run.
func TestVetoedTerminalTool(t *testing.T) {
	submit := tool.AsTerminal(tool.New("submit", "", func(_ context.Context, in numIn) (int, error) { return in.N, nil }))
	p := fake.New(fake.ToolCalls(fake.Call("v1", "submit", numIn{1})), fake.Text("recovered"))
	a, sess := newAgent(t, p, func(c *agentkit.Config) {
		c.Tools = tool.NewSet(submit)
		c.Hooks.BeforeToolCall = func(context.Context, *agentkit.ToolCall) error { return errors.New("denied") }
	})
	res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
	if err != nil || res.Status != store.RunCompleted || res.Text != "recovered" || res.Steps != 2 {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// Cancellation still wins over terminal completion: a run abandoned during a terminal tool
// call is cancelled, not completed, and its tool result stays persisted.
func TestTerminalToolCancelled(t *testing.T) {
	blocked := make(chan struct{})
	submit := tool.AsTerminal(tool.New("submit", "", func(ctx context.Context, _ struct{}) (string, error) {
		close(blocked)
		<-ctx.Done()
		return "", ctx.Err()
	}))
	p := fake.New(fake.ToolCalls(fake.Call("c1", "submit", struct{}{})), fake.Text("unused"))
	a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(submit) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-blocked; cancel() }()
	evs, err := collect(a.Run(ctx, sess.ID, agentkit.UserText("go")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if last := evs[len(evs)-1]; last.Type != agentkit.EventRunFailed || last.Status != store.RunCancelled {
		t.Fatalf("last event = %+v", last)
	}
	h := history(t, a, sess.ID)
	if len(h) != 3 || !toolResults(t, h[2])[0].IsError {
		t.Fatalf("history = %+v", h)
	}
}

// Max steps still applies to non-terminal loops, and a terminal run counts its steps as usual.
func TestTerminalToolMaxSteps(t *testing.T) {
	t.Run("non-terminal loop still hits the limit", func(t *testing.T) {
		echo := tool.New("echo", "", func(_ context.Context, in numIn) (int, error) { return in.N, nil })
		p := fake.New(
			fake.ToolCalls(fake.Call("a", "echo", numIn{1})),
			fake.ToolCalls(fake.Call("b", "echo", numIn{2})),
			fake.Text("never reached"),
		)
		a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(echo); c.MaxSteps = 2 })
		_, err := collect(a.Run(context.Background(), sess.ID, agentkit.UserText("go")))
		if !errors.Is(err, agentkit.ErrMaxSteps) {
			t.Fatalf("err = %v", err)
		}
		if p.Remaining() != 1 || len(p.Requests()) != 2 {
			t.Fatalf("provider calls = %d, remaining = %d", len(p.Requests()), p.Remaining())
		}
	})
	t.Run("terminal run after a non-terminal step", func(t *testing.T) {
		echo := tool.New("echo", "", func(_ context.Context, in numIn) (int, error) { return in.N, nil })
		submit := tool.AsTerminal(tool.New("submit", "", func(_ context.Context, in numIn) (string, error) {
			return "the answer", nil
		}))
		p := fake.New(
			fake.ToolCalls(fake.Call("a", "echo", numIn{1})),
			fake.ToolCalls(fake.Call("b", "submit", numIn{2})),
			fake.Text("never reached"),
		)
		a, sess := newAgent(t, p, func(c *agentkit.Config) { c.Tools = tool.NewSet(echo, submit) })
		res, err := a.RunSync(context.Background(), sess.ID, agentkit.UserText("go"))
		if err != nil || res.Status != store.RunCompleted || res.Text != "the answer" || res.Steps != 2 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if p.Remaining() != 1 {
			t.Fatal("provider consumed the follow-up turn")
		}
	})
}
