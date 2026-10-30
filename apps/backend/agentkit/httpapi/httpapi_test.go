package httpapi_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/httpapi"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
	memory "github.com/dot5enko/wonder-critique/apps/backend/agentkit/store/memory"
)

func newAgent(t *testing.T, turns ...fake.Turn) (*agentkit.Agent, *fake.Provider) {
	t.Helper()
	p := fake.New(turns...)
	a, err := agentkit.New(agentkit.Config{Name: "test", Provider: p, Store: memory.New()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, p
}

func newServer(t *testing.T, a *agentkit.Agent, owner func(*http.Request) (string, error)) http.Handler {
	t.Helper()
	return httpapi.New(a, httpapi.Options{SessionOwner: owner, Heartbeat: 50 * time.Millisecond})
}

// sse frames from an SSE response body.
type sseFrame struct{ event, data string }

func readSSE(t *testing.T, r io.Reader) []sseFrame {
	t.Helper()
	var out []sseFrame
	sc := bufio.NewScanner(r)
	var ev sseFrame
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
			out = append(out, ev)
			ev = sseFrame{}
		}
	}
	return out
}

func TestSessionsCRUD(t *testing.T) {
	a, _ := newAgent(t, fake.Text("hi"))
	h := newServer(t, a, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := srv.Client().Post(srv.URL+"/sessions", "application/json", strings.NewReader(`{"title":"t1","metadata":{"k":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var sess store.Session
	if res.StatusCode != 201 || json.NewDecoder(res.Body).Decode(&sess) != nil {
		t.Fatalf("create = %d", res.StatusCode)
	}
	res.Body.Close()
	if sess.Title != "t1" || sess.Metadata["k"] != "v" {
		t.Fatalf("session = %+v", sess)
	}

	res, _ = srv.Client().Get(srv.URL + "/sessions")
	var list struct {
		Sessions []store.Session `json:"sessions"`
	}
	if json.NewDecoder(res.Body).Decode(&list) != nil || len(list.Sessions) != 1 {
		t.Fatalf("list = %+v", list)
	}
	res.Body.Close()

	res, _ = srv.Client().Get(srv.URL + "/sessions/" + sess.ID)
	if res.StatusCode != 200 {
		t.Fatalf("get = %d", res.StatusCode)
	}
	var got struct {
		Messages []store.StoredMessage `json:"messages"`
	}
	if json.NewDecoder(res.Body).Decode(&got) != nil || len(got.Messages) != 0 {
		t.Fatalf("get body = %+v", got)
	}
	res.Body.Close()

	req, _ := http.NewRequest("DELETE", srv.URL+"/sessions/"+sess.ID, nil)
	if res, _ = srv.Client().Do(req); res.StatusCode != 204 {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	res, _ = srv.Client().Get(srv.URL + "/sessions/" + sess.ID)
	if res.StatusCode != 404 {
		t.Fatalf("get after delete = %d", res.StatusCode)
	}
}

func TestStreamRun(t *testing.T) {
	a, _ := newAgent(t, fake.Turn{Message: llm.AssistantText("hello world"), Stream: true})
	srv := httptest.NewServer(newServer(t, a, nil))
	defer srv.Close()

	sess := mustSession(t, srv)
	res, err := srv.Client().Post(srv.URL+"/sessions/"+sess.ID+"/runs", "application/json", strings.NewReader(`{"input":"go"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	frames := readSSE(t, res.Body)

	var text string
	kinds := map[string]bool{}
	for _, f := range frames {
		if f.event == "done" {
			continue
		}
		kinds[f.event] = true
		if f.event == "text_delta" {
			var ev agentkit.Event
			if json.Unmarshal([]byte(f.data), &ev) == nil {
				text += ev.Delta
			}
		}
	}
	if text != "hello world" {
		t.Fatalf("streamed text = %q", text)
	}
	for _, want := range []string{"run_started", "step_finished", "run_finished"} {
		if !kinds[want] {
			t.Errorf("missing %q event, got %v", want, kinds)
		}
	}
}

func TestSyncRun(t *testing.T) {
	a, _ := newAgent(t, fake.Text("answer"))
	srv := httptest.NewServer(newServer(t, a, nil))
	defer srv.Close()

	sess := mustSession(t, srv)
	res, err := srv.Client().Post(srv.URL+"/sessions/"+sess.ID+"/runs?stream=false", "application/json", strings.NewReader(`{"input":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil || out.Text != "answer" || out.Status != "completed" || out.RunID == "" {
		t.Fatalf("run response = %+v", out)
	}
}

func TestBusyAndCancel(t *testing.T) {
	a, _ := newAgent(t, fake.Turn{Hang: true})
	srv := httptest.NewServer(newServer(t, a, nil))
	defer srv.Close()

	sess := mustSession(t, srv)
	url := srv.URL + "/sessions/" + sess.ID + "/runs"

	streamed := make(chan struct{})
	go func() {
		res, err := srv.Client().Post(url, "application/json", strings.NewReader(`{"input":"go"}`))
		if err != nil {
			return
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body) // read until the run ends
		close(streamed)
	}()
	time.Sleep(100 * time.Millisecond) // let the first run start

	if res, _ := srv.Client().Post(url, "application/json", strings.NewReader(`{"input":"again"}`)); res.StatusCode != 409 {
		t.Errorf("second run = %d, want 409", res.StatusCode)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/sessions/"+sess.ID+"/cancel", nil)
	if res, _ := srv.Client().Do(req); res.StatusCode != 204 {
		t.Errorf("cancel = %d, want 204", res.StatusCode)
	}
	select {
	case <-streamed:
	case <-time.After(2 * time.Second):
		t.Error("first run did not finish after cancel")
	}
}

func TestOwnerIsolation(t *testing.T) {
	a, _ := newAgent(t, fake.Text("ok"))
	owner := func(r *http.Request) (string, error) { return r.Header.Get("X-User"), nil }
	srv := httptest.NewServer(newServer(t, a, owner))
	defer srv.Close()

	var mine store.Session
	req, _ := http.NewRequest("POST", srv.URL+"/sessions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "u1")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 201 || json.Unmarshal(body, &mine) != nil {
		t.Fatalf("create session = %d %s", res.StatusCode, body)
	}
	res.Body.Close()

	// Other users cannot see, use or delete it.
	asOther := func(method, url string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, url, nil)
		r.Header.Set("X-User", "u2")
		res, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := asOther("GET", srv.URL+"/sessions/"+mine.ID); res.StatusCode != 404 {
		t.Errorf("get as other = %d, want 404", res.StatusCode)
	}
	if res := asOther("POST", srv.URL+"/sessions/"+mine.ID+"/runs?stream=false"); res.StatusCode != 404 {
		t.Errorf("run as other = %d, want 404", res.StatusCode)
	}
	if res := asOther("DELETE", srv.URL+"/sessions/"+mine.ID); res.StatusCode != 404 {
		t.Errorf("delete as other = %d, want 404", res.StatusCode)
	}

	// The owner sees it and the list is filtered.
	req, _ = http.NewRequest("GET", srv.URL+"/sessions/"+mine.ID, nil)
	req.Header.Set("X-User", "u1")
	res, _ = srv.Client().Do(req)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("get as owner = %d, want 200", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/sessions", nil)
	req.Header.Set("X-User", "u2")
	res, _ = srv.Client().Do(req)
	var list struct {
		Sessions []store.Session `json:"sessions"`
	}
	json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if len(list.Sessions) != 0 {
		t.Errorf("list as other = %+v, want empty", list.Sessions)
	}
}

func TestOwnerRequired(t *testing.T) {
	a, _ := newAgent(t, fake.Text("ok"))
	owner := func(r *http.Request) (string, error) {
		if r.Header.Get("X-User") == "" {
			return "", errors.New("no user")
		}
		return r.Header.Get("X-User"), nil
	}
	srv := httptest.NewServer(newServer(t, a, owner))
	defer srv.Close()

	res, _ := srv.Client().Post(srv.URL+"/sessions", "application/json", strings.NewReader(`{}`))
	if res.StatusCode != 401 {
		t.Errorf("create without user = %d, want 401", res.StatusCode)
	}
}

func TestMountedUnderPrefix(t *testing.T) {
	a, _ := newAgent(t, fake.Text("ok"))
	srv := httptest.NewServer(http.StripPrefix("/api/agents/test", newServer(t, a, nil)))
	defer srv.Close()

	res, _ := srv.Client().Post(srv.URL+"/api/agents/test/sessions", "application/json", strings.NewReader(`{}`))
	if res.StatusCode != 201 {
		t.Errorf("create under prefix = %d, want 201", res.StatusCode)
	}
}

func mustSession(t *testing.T, srv *httptest.Server) store.Session {
	t.Helper()
	res, err := srv.Client().Post(srv.URL+"/sessions", "application/json", strings.NewReader(`{"title":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sess store.Session
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 201 || json.Unmarshal(body, &sess) != nil {
		t.Fatalf("create session = %d %s", res.StatusCode, body)
	}
	return sess
}
