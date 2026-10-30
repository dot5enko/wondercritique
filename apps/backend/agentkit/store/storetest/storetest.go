// Package storetest is a conformance suite for store.Store implementations.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
)

// Run executes the conformance suite. newStore must return a fresh, empty store for each call.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, store.Store)
	}{
		{"Sessions", testSessions},
		{"ListSessions", testListSessions},
		{"Messages", testMessages},
		{"ConcurrentAppend", testConcurrentAppend},
		{"Runs", testRuns},
		{"ToolCalls", testToolCalls},
		{"DeleteSession", testDeleteSession},
		{"RecoverInterrupted", testRecoverInterrupted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, newStore(t)) })
	}
}

func timeClose(a, b time.Time) bool { d := a.Sub(b); return d < time.Second && d > -time.Second }

func mustSession(t *testing.T, s store.Store, sess *store.Session) *store.Session {
	t.Helper()
	if err := s.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess
}

func testSessions(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a", Title: "hello", Metadata: map[string]any{"user_id": "u1"}})
	if sess.ID == "" || sess.CreatedAt.IsZero() || sess.UpdatedAt.IsZero() {
		t.Fatalf("CreateSession did not assign defaults: %+v", sess)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != sess.ID || got.Agent != "a" || got.Title != "hello" || got.Metadata["user_id"] != "u1" {
		t.Fatalf("GetSession = %+v", got)
	}
	if !timeClose(got.CreatedAt, sess.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, sess.CreatedAt)
	}
	explicit := mustSession(t, s, &store.Session{ID: "explicit-id", Agent: "a"})
	if explicit.ID != "explicit-id" {
		t.Fatalf("explicit ID overwritten: %q", explicit.ID)
	}
	if _, err := s.GetSession(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSession(missing) err = %v, want ErrNotFound", err)
	}
}

func testListSessions(t *testing.T, s store.Store) {
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	for i := range 5 {
		agent, owner := "a", "u1"
		if i%2 == 1 {
			owner = "u2"
		}
		if i == 4 {
			agent = "b"
		}
		mustSession(t, s, &store.Session{
			ID: fmt.Sprintf("s%d", i), Agent: agent, Title: fmt.Sprint(i),
			Metadata: map[string]any{"user_id": owner}, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	ids := func(opts store.ListOptions) []string {
		t.Helper()
		list, err := s.ListSessions(ctx, opts)
		if err != nil {
			t.Fatalf("ListSessions(%+v): %v", opts, err)
		}
		out := []string{}
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	check := func(opts store.ListOptions, want ...string) {
		t.Helper()
		if want == nil {
			want = []string{}
		}
		if got := ids(opts); !reflect.DeepEqual(got, want) {
			t.Errorf("ListSessions(%+v) = %v, want %v", opts, got, want)
		}
	}
	check(store.ListOptions{}, "s4", "s3", "s2", "s1", "s0")
	check(store.ListOptions{Agent: "a"}, "s3", "s2", "s1", "s0")
	check(store.ListOptions{Agent: "a", Metadata: map[string]any{"user_id": "u1"}}, "s2", "s0")
	check(store.ListOptions{Metadata: map[string]any{"user_id": "nobody"}})
	check(store.ListOptions{Metadata: map[string]any{"missing": "x"}})
	check(store.ListOptions{Limit: 2}, "s4", "s3")
	check(store.ListOptions{Limit: 2, Offset: 2}, "s2", "s1")
	check(store.ListOptions{Offset: 10})
}

func sampleMessages() []llm.Message {
	return []llm.Message{
		llm.UserText("hi"),
		llm.NewMessage(llm.RoleAssistant,
			llm.ReasoningBlock("meta", json.RawMessage(`{"type":"reasoning","encrypted_content":"abc","summary":[]}`), "thinking"),
			llm.Block{Type: llm.BlockText, Text: "let me check", Phase: llm.PhaseCommentary},
			llm.ToolCallBlock("call_1", "lookup", json.RawMessage(`{"q":"x"}`)),
		),
		llm.NewMessage(llm.RoleTool, llm.ToolResultBlock("call_1", "lookup", "boom", true)),
		llm.NewMessage(llm.RoleUser, llm.TextBlock("look"), llm.ImageBlock("data:image/png;base64,AAAA")),
	}
}

func testMessages(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a", CreatedAt: time.Now().UTC().Add(-time.Hour)})
	if err := s.AppendMessages(ctx, "missing", "r", llm.UserText("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AppendMessages(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.History(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("History(missing) err = %v, want ErrNotFound", err)
	}
	if h, err := s.History(ctx, sess.ID); err != nil || len(h) != 0 {
		t.Fatalf("History(empty) = %v, %v", h, err)
	}
	msgs := sampleMessages()
	if err := s.AppendMessages(ctx, sess.ID, "r1", msgs[:2]...); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if err := s.AppendMessages(ctx, sess.ID, "r2", msgs[2:]...); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if err := s.AppendMessages(ctx, sess.ID, "r2"); err != nil {
		t.Fatalf("AppendMessages(no messages): %v", err)
	}
	hist, err := s.History(ctx, sess.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != len(msgs) {
		t.Fatalf("History len = %d, want %d", len(hist), len(msgs))
	}
	seen := map[string]bool{}
	for i, sm := range hist {
		if sm.Seq != int64(i+1) || sm.SessionID != sess.ID || sm.ID == "" || seen[sm.ID] || sm.CreatedAt.IsZero() {
			t.Errorf("hist[%d] = %+v", i, sm)
		}
		seen[sm.ID] = true
		wantRun := "r1"
		if i >= 2 {
			wantRun = "r2"
		}
		if sm.RunID != wantRun {
			t.Errorf("hist[%d].RunID = %q, want %q", i, sm.RunID, wantRun)
		}
		if !reflect.DeepEqual(sm.Message, msgs[i]) {
			t.Errorf("hist[%d].Message =\n%#v\nwant\n%#v", i, sm.Message, msgs[i])
		}
	}
	// Returned history must not alias store state.
	hist[1].Message.Blocks[0].Opaque[0] = 'X'
	hist[1].Message.Blocks[1].Text = "mutated"
	again, _ := s.History(ctx, sess.ID)
	if !reflect.DeepEqual(again[1].Message, msgs[1]) {
		t.Errorf("history aliased store state")
	}
	got, _ := s.GetSession(ctx, sess.ID)
	if !got.UpdatedAt.After(sess.CreatedAt) {
		t.Errorf("UpdatedAt not bumped: %v (created %v)", got.UpdatedAt, sess.CreatedAt)
	}
}

func testConcurrentAppend(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a"})
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			if err := s.AppendMessages(ctx, sess.ID, "r", llm.UserText(fmt.Sprint(i)), llm.AssistantText("ok")); err != nil {
				t.Errorf("AppendMessages: %v", err)
			}
		})
	}
	wg.Wait()
	hist, err := s.History(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 20 {
		t.Fatalf("len = %d, want 20", len(hist))
	}
	for i, sm := range hist {
		if sm.Seq != int64(i+1) {
			t.Fatalf("hist[%d].Seq = %d", i, sm.Seq)
		}
		if i%2 == 1 && sm.Message.Role != llm.RoleAssistant {
			t.Fatalf("batch not contiguous at %d", i)
		}
	}
}

func testRuns(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a"})
	r := &store.Run{SessionID: sess.ID, Agent: "a", Model: "m", Status: store.RunRunning}
	if err := s.SaveRun(ctx, r); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if r.ID == "" || r.StartedAt.IsZero() {
		t.Fatalf("SaveRun did not assign defaults: %+v", r)
	}
	got, err := s.GetRun(ctx, r.ID)
	if err != nil || got.Status != store.RunRunning || got.Model != "m" || !got.FinishedAt.IsZero() {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
	r.Status, r.Error = store.RunFailed, "boom"
	r.Usage = llm.Usage{InputTokens: 1, CachedInputTokens: 2, OutputTokens: 3, ReasoningTokens: 4}
	r.FinishedAt = time.Now().UTC()
	if err := s.SaveRun(ctx, r); err != nil {
		t.Fatalf("SaveRun(update): %v", err)
	}
	got, _ = s.GetRun(ctx, r.ID)
	if got.Status != store.RunFailed || got.Error != "boom" || got.Usage != r.Usage || !timeClose(got.FinishedAt, r.FinishedAt) {
		t.Fatalf("updated run = %+v", got)
	}
	r2 := &store.Run{SessionID: sess.ID, Agent: "a", Status: store.RunCompleted, StartedAt: r.StartedAt.Add(time.Second)}
	if err := s.SaveRun(ctx, r2); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(ctx, sess.ID)
	if err != nil || len(runs) != 2 || runs[0].ID != r.ID || runs[1].ID != r2.ID {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	if _, err := s.GetRun(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun(missing) err = %v", err)
	}
}

func testToolCalls(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a"})
	r := &store.Run{SessionID: sess.ID, Agent: "a", Status: store.RunRunning}
	if err := s.SaveRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	recs := []*store.ToolCallRecord{
		{RunID: r.ID, SessionID: sess.ID, CallID: "c1", Name: "add", Input: json.RawMessage(`{"a":1}`), Output: "2", Duration: 1500 * time.Millisecond},
		{RunID: r.ID, SessionID: sess.ID, CallID: "c2", Name: "fail", Input: json.RawMessage(`{}`), Output: "bad", IsError: true},
	}
	for _, rec := range recs {
		if err := s.SaveToolCall(ctx, rec); err != nil {
			t.Fatalf("SaveToolCall: %v", err)
		}
		if rec.ID == "" || rec.CreatedAt.IsZero() {
			t.Fatalf("SaveToolCall did not assign defaults: %+v", rec)
		}
	}
	got, err := s.ListToolCalls(ctx, r.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListToolCalls = %+v, %v", got, err)
	}
	for i, g := range got {
		w := recs[i]
		if g.ID != w.ID || g.CallID != w.CallID || g.Name != w.Name || string(g.Input) != string(w.Input) ||
			g.Output != w.Output || g.IsError != w.IsError || g.Duration.Milliseconds() != w.Duration.Milliseconds() {
			t.Errorf("tool call %d = %+v, want %+v", i, g, w)
		}
	}
}

func testDeleteSession(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a"})
	other := mustSession(t, s, &store.Session{Agent: "a"})
	for _, id := range []string{sess.ID, other.ID} {
		r := &store.Run{ID: "run-" + id, SessionID: id, Agent: "a", Status: store.RunCompleted}
		if err := s.SaveRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendMessages(ctx, id, r.ID, llm.UserText("x")); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveToolCall(ctx, &store.ToolCallRecord{RunID: r.ID, SessionID: id, CallID: "c", Name: "n", Input: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession(ctx, sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetSession after delete err = %v", err)
	}
	if _, err := s.GetRun(ctx, "run-"+sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun after delete err = %v", err)
	}
	if tc, _ := s.ListToolCalls(ctx, "run-"+sess.ID); len(tc) != 0 {
		t.Fatalf("tool calls survived delete: %+v", tc)
	}
	if err := s.DeleteSession(ctx, sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteSession err = %v", err)
	}
	if h, err := s.History(ctx, other.ID); err != nil || len(h) != 1 {
		t.Fatalf("other session affected: %v, %v", h, err)
	}
	if tc, _ := s.ListToolCalls(ctx, "run-"+other.ID); len(tc) != 1 {
		t.Fatalf("other tool calls affected: %+v", tc)
	}
}

func testRecoverInterrupted(t *testing.T, s store.Store) {
	ctx := context.Background()
	sess := mustSession(t, s, &store.Session{Agent: "a"})
	statuses := []store.RunStatus{store.RunRunning, store.RunCompleted, store.RunRunning, store.RunFailed}
	ids := make([]string, len(statuses))
	for i, st := range statuses {
		r := &store.Run{SessionID: sess.ID, Agent: "a", Status: st}
		if err := s.SaveRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		ids[i] = r.ID
	}
	n, err := s.RecoverInterrupted(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RecoverInterrupted = %d, %v; want 2", n, err)
	}
	want := []store.RunStatus{store.RunInterrupted, store.RunCompleted, store.RunInterrupted, store.RunFailed}
	for i, id := range ids {
		r, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != want[i] {
			t.Errorf("run %d status = %s, want %s", i, r.Status, want[i])
		}
		if want[i] == store.RunInterrupted && r.FinishedAt.IsZero() {
			t.Errorf("run %d FinishedAt not set", i)
		}
	}
	if n, _ := s.RecoverInterrupted(ctx); n != 0 {
		t.Fatalf("second RecoverInterrupted = %d, want 0", n)
	}
}
