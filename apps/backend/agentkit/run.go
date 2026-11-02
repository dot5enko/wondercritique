package agentkit

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

// Result summarizes a finished run.
type Result struct {
	RunID    string
	Status   store.RunStatus
	Text     string        // final answer: the last assistant message's text, or a terminal tool's output when one ended the run
	Usage    llm.Usage     // aggregated over all steps
	Steps    int           // model calls made
	Messages []llm.Message // messages produced by the run (assistant and tool results), in order
}

// Run executes the agent loop in a session, appending input to its history. The run starts when
// the returned sequence is iterated. Events are yielded with a nil error; a failed or cancelled
// run ends with a run_failed event paired with the error. Breaking out of the loop cancels the run.
func (a *Agent) Run(ctx context.Context, sessionID string, input ...llm.Message) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) { a.run(ctx, sessionID, input, yield) }
}

// RunSync executes a run to completion. On failure it returns the partial result with the error.
func (a *Agent) RunSync(ctx context.Context, sessionID string, input ...llm.Message) (*Result, error) {
	return a.run(ctx, sessionID, input, func(Event, error) bool { return true })
}

type runner struct {
	a       *Agent
	ctx     context.Context // cancelled on Cancel, parent cancellation or consumer break
	cancel  context.CancelFunc
	bg      context.Context // for persistence; survives cancellation
	yield   func(Event, error) bool
	stopped bool
	sess    *store.Session
	run     *store.Run
	res     *Result
	history []llm.Message
}

func (a *Agent) run(ctx context.Context, sessionID string, input []llm.Message, yield func(Event, error) bool) (*Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &runner{a: a, ctx: ctx, cancel: cancel, bg: context.WithoutCancel(ctx), yield: yield, res: &Result{}}
	r.run = &store.Run{SessionID: sessionID, Agent: a.cfg.Name, Model: a.cfg.Model}

	if !a.acquire(sessionID, cancel) {
		return r.res, r.fail(ErrSessionBusy)
	}
	defer a.release(sessionID)
	sess, err := a.GetSession(ctx, sessionID)
	if err != nil {
		return r.res, r.fail(err)
	}
	r.sess = sess
	r.run.Status = store.RunRunning
	r.run.StartedAt = time.Now().UTC()
	if err := a.cfg.Store.SaveRun(r.bg, r.run); err != nil {
		return r.res, r.fail(fmt.Errorf("agentkit: save run: %w", err))
	}
	r.res.RunID = r.run.ID
	r.emit(Event{Type: EventRunStarted})

	err = r.loop(input)
	r.res.Status = store.RunCompleted
	if err != nil {
		r.res.Status = store.RunFailed
		if ctx.Err() != nil {
			r.res.Status, err = store.RunCancelled, ctx.Err()
		}
		r.run.Error = err.Error()
	}
	r.run.Status = r.res.Status
	r.run.Usage = r.res.Usage
	r.run.FinishedAt = time.Now().UTC()
	if serr := a.cfg.Store.SaveRun(r.bg, r.run); serr != nil {
		err = errors.Join(err, fmt.Errorf("agentkit: save run: %w", serr))
	}
	if err != nil {
		return r.res, r.fail(err)
	}
	u := r.res.Usage
	r.emit(Event{Type: EventRunFinished, Text: r.res.Text, Usage: &u, Status: r.res.Status})
	return r.res, nil
}

func (r *runner) loop(input []llm.Message) error {
	cfg := &r.a.cfg
	if err := cfg.Store.AppendMessages(r.bg, r.sess.ID, r.run.ID, input...); err != nil {
		return fmt.Errorf("agentkit: append input: %w", err)
	}
	stored, err := cfg.Store.History(r.bg, r.sess.ID)
	if err != nil {
		return fmt.Errorf("agentkit: load history: %w", err)
	}
	for _, sm := range stored {
		r.history = append(r.history, sm.Message)
	}
	specs := cfg.Tools.Specs()
	for step := 1; ; step++ {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if step > cfg.MaxSteps {
			return ErrMaxSteps
		}
		msgs, err := cfg.ContextPolicy.Messages(r.ctx, r.sess, r.history)
		if err != nil {
			return fmt.Errorf("agentkit: context policy: %w", err)
		}
		req := &llm.Request{
			Model: cfg.Model, System: cfg.System, Messages: msgs, Tools: specs,
			MaxOutputTokens: cfg.MaxOutputTokens, ReasoningEffort: cfg.ReasoningEffort, Extra: cfg.Extra,
		}
		if h := cfg.Hooks.BeforeRequest; h != nil {
			if err := h(r.ctx, r.sess, req); err != nil {
				return err
			}
		}
		r.res.Steps = step
		msg, usage, stop, err := r.stream(step, req)
		if err != nil {
			return err
		}
		r.res.Usage.Add(usage)
		if err := r.append(msg); err != nil {
			return err
		}
		r.emit(Event{Type: EventStepFinished, Step: step, Message: &msg, Usage: &usage, StopReason: stop})

		calls := msg.ToolCalls()
		if len(calls) == 0 {
			r.res.Text = msg.Text()
			return nil
		}
		results, ended := r.execTools(step, calls)
		if err := r.append(llm.NewMessage(llm.RoleTool, results...)); err != nil {
			return err
		}
		if ended != nil {
			// A terminal tool executed: its output is the run's final answer and no further model
			// call happens. Cancellation still takes precedence so an abandoned run is not completed.
			if err := r.ctx.Err(); err != nil {
				return err
			}
			r.res.Text = ended.out
			return nil
		}
	}
}

// append persists msg and adds it to the in-memory history and result.
func (r *runner) append(msg llm.Message) error {
	if err := r.a.cfg.Store.AppendMessages(r.bg, r.sess.ID, r.run.ID, msg); err != nil {
		return fmt.Errorf("agentkit: append message: %w", err)
	}
	r.history = append(r.history, msg)
	r.res.Messages = append(r.res.Messages, msg)
	return nil
}

func (r *runner) stream(step int, req *llm.Request) (llm.Message, llm.Usage, string, error) {
	var (
		done  *llm.Message
		usage llm.Usage
		stop  string
	)
	for se, err := range r.a.cfg.Provider.Stream(r.ctx, req) {
		if err != nil {
			return llm.Message{}, usage, "", fmt.Errorf("agentkit: model: %w", err)
		}
		ok := true
		switch se.Type {
		case llm.StreamTextDelta:
			ok = r.emit(Event{Type: EventTextDelta, Step: step, Delta: se.Delta})
		case llm.StreamReasoningSummaryDelta:
			ok = r.emit(Event{Type: EventReasoningDelta, Step: step, Delta: se.Delta})
		case llm.StreamUsage:
			if se.Usage != nil {
				usage = *se.Usage
			}
		case llm.StreamMessageDone:
			done, stop = se.Message, se.StopReason
			if se.Usage != nil {
				usage = *se.Usage
			}
		}
		if !ok {
			break
		}
	}
	if err := r.ctx.Err(); err != nil {
		return llm.Message{}, usage, "", err
	}
	if done == nil {
		return llm.Message{}, usage, "", errors.New("agentkit: model stream ended without message_done")
	}
	msg := done.Clone()
	msg.Role = llm.RoleAssistant
	return msg, usage, stop, nil
}

type toolOutcome struct {
	i        int
	call     *ToolCall
	out      string
	isErr    bool
	terminal bool // the call reached a terminal tool, so its output ends the run
	duration time.Duration
}

// isTerminalTool reports whether executing t ends the run (see tool.Terminal).
func isTerminalTool(t tool.Tool) bool {
	c, ok := t.(tool.Terminal)
	return ok && c.IsTerminal()
}

// execTools runs calls in parallel and returns tool_result blocks in call order. When a call
// reached a terminal tool (unknown tools and vetoed calls do not count as executed), the second
// return is that call's outcome — the first one in call order — whose output ends the run.
func (r *runner) execTools(step int, calls []llm.Block) ([]llm.Block, *toolOutcome) {
	cfg := &r.a.cfg
	ch := make(chan toolOutcome, len(calls))
	for i, c := range calls {
		tc := &ToolCall{ID: c.ToolCallID, Name: c.ToolName, Input: c.Input, SessionID: r.sess.ID, RunID: r.run.ID, Step: step}
		r.emit(Event{Type: EventToolCallStarted, Step: step, ToolCall: &ToolCallEvent{ID: tc.ID, Name: tc.Name, Input: tc.Input}})
		t, ok := cfg.Tools.Get(tc.Name)
		if !ok {
			ch <- toolOutcome{i: i, call: tc, out: fmt.Sprintf("unknown tool %q", tc.Name), isErr: true}
			continue
		}
		terminal := isTerminalTool(t)
		if h := cfg.Hooks.BeforeToolCall; h != nil {
			if err := h(r.ctx, tc); err != nil {
				ch <- toolOutcome{i: i, call: tc, out: "tool call rejected: " + err.Error(), isErr: true}
				continue
			}
		}
		go func() {
			start := time.Now()
			out, err := r.callTool(t, tc)
			o := toolOutcome{i: i, call: tc, out: out, terminal: terminal, duration: time.Since(start)}
			if err != nil {
				o.out, o.isErr = err.Error(), true
			}
			ch <- o
		}()
	}
	results := make([]llm.Block, len(calls))
	var ended *toolOutcome
	for range calls {
		o := <-ch
		tc := o.call
		results[o.i] = llm.ToolResultBlock(tc.ID, tc.Name, o.out, o.isErr)
		_ = cfg.Store.SaveToolCall(r.bg, &store.ToolCallRecord{
			RunID: r.run.ID, SessionID: r.sess.ID, CallID: tc.ID, Name: tc.Name, Input: tc.Input,
			Output: o.out, IsError: o.isErr, Duration: o.duration,
		})
		r.emit(Event{Type: EventToolCallFinished, Step: step, ToolCall: &ToolCallEvent{
			ID: tc.ID, Name: tc.Name, Input: tc.Input, Output: o.out, IsError: o.isErr, Duration: o.duration,
		}})
		if o.terminal && (ended == nil || o.i < ended.i) {
			ended = &o
		}
	}
	return results, ended
}

// callTool runs one tool with the configured timeout, recovering panics. A tool that ignores
// its context is abandoned when the timeout fires.
func (r *runner) callTool(t tool.Tool, tc *ToolCall) (string, error) {
	timeout := r.a.cfg.ToolTimeout
	ctx, cancel := context.WithTimeout(context.WithValue(r.ctx, callKey{}, tc), timeout)
	defer cancel()
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- result{err: fmt.Errorf("tool %s panicked: %v", tc.Name, p)}
			}
		}()
		out, err := t.Call(ctx, tc.Input)
		ch <- result{out, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		res.err = ctx.Err()
	}
	if res.err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && r.ctx.Err() == nil {
		res.err = fmt.Errorf("tool %s timed out after %s", tc.Name, timeout)
	}
	return res.out, res.err
}

// emit delivers an event to OnEvent and the consumer. It reports false once the consumer stopped.
func (r *runner) emit(ev Event) bool {
	r.fill(&ev)
	if h := r.a.cfg.Hooks.OnEvent; h != nil {
		h(ev)
	}
	if r.stopped {
		return false
	}
	if !r.yield(ev, nil) {
		r.stopped = true
		r.cancel()
	}
	return !r.stopped
}

// fail emits the terminal run_failed event paired with err and returns err.
func (r *runner) fail(err error) error {
	u := r.res.Usage
	ev := Event{Type: EventRunFailed, Error: err.Error(), Usage: &u, Status: r.res.Status}
	r.fill(&ev)
	if h := r.a.cfg.Hooks.OnEvent; h != nil {
		h(ev)
	}
	if !r.stopped {
		r.stopped = true
		r.yield(ev, err)
	}
	return err
}

func (r *runner) fill(ev *Event) {
	ev.SessionID, ev.RunID = r.run.SessionID, r.run.ID
}
