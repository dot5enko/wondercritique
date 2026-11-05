package engine

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// newGuide inserts a style guide of owner with the given status.
func (h *harness) newGuide(owner, status string) string {
	h.t.Helper()
	sg := models.Styleguide{OwnerID: owner, Name: "Acme", Markdown: "# Acme", Status: status}
	if err := h.e.DB.Create(&sg).Error; err != nil {
		h.t.Fatal(err)
	}
	return sg.ID
}

// The first message picks the chat's style guide; follow-ups inherit it and
// can't switch to another one.
func TestStyleguideIsPinnedPerThread(t *testing.T) {
	h := setup(t)
	h.chatScript(1, "")
	guide := h.newGuide("user1", models.StyleguideReady)
	other := h.newGuide("user1", models.StyleguideReady)

	id, w := h.start(h.user, `{"agent":"canvas-chat","styleguide_id":"`+guide+`","input":{"brief":"a card"}}`)
	if id == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if run := h.waitDone(id); run.StyleguideID != guide || run.Status != models.RunCompleted {
		t.Fatalf("run = %s guide=%q err=%q", run.Status, run.StyleguideID, run.Error)
	}
	if calls := h.agents.callsOf(studio.ChatAgentSlug); len(calls) != 1 || calls[0].StyleguideID != guide {
		t.Fatalf("agent got %+v", calls)
	}

	// A follow-up without styleguide_id (or with the same one) inherits it.
	for _, extra := range []string{``, `"styleguide_id":"` + guide + `",`} {
		second, w := h.start(h.user, `{"agent":"canvas-chat",`+extra+`"thread_id":"`+id+`","input":{"brief":"again"}}`)
		if second == "" {
			t.Fatalf("follow-up: %d %s", w.Code, w.Body)
		}
		if run := h.waitDone(second); run.StyleguideID != guide {
			t.Errorf("follow-up guide = %q", run.StyleguideID)
		}
	}
	calls := h.agents.callsOf(studio.ChatAgentSlug)
	if calls[len(calls)-1].StyleguideID != guide {
		t.Errorf("follow-up agent guide = %q", calls[len(calls)-1].StyleguideID)
	}

	// Switching is rejected.
	_, w = h.start(h.user, `{"agent":"canvas-chat","styleguide_id":"`+other+`","thread_id":"`+id+`","input":{"brief":"x"}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "can't be changed") {
		t.Errorf("switch = %d %s", w.Code, w.Body)
	}

	// The thread list and run views carry it.
	if w := h.do("GET", "/threads", "", h.user); !strings.Contains(w.Body.String(), `"styleguide_id":"`+guide+`"`) {
		t.Errorf("threads = %s", w.Body)
	}
	if d := h.detail(id); d.StyleguideID != guide {
		t.Errorf("detail guide = %q", d.StyleguideID)
	}
}

func TestStyleguideValidationOnNewThreads(t *testing.T) {
	h := setup(t)
	h.chatScript(1, "")
	cases := map[string]string{
		"unknown":        models.NewID(),
		"someone else's": h.newGuide("user2", models.StyleguideReady),
		"generating":     h.newGuide("user1", models.StyleguideGenerating),
		"failed":         h.newGuide("user1", models.StyleguideFailed),
	}
	for name, sg := range cases {
		_, w := h.start(h.user, `{"agent":"canvas-chat","styleguide_id":"`+sg+`","input":{"brief":"a card"}}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "styleguide_id") {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	// Admins may use any ready guide.
	if id, w := h.start(h.admin, `{"agent":"canvas-chat","styleguide_id":"`+cases["someone else's"]+`","input":{"brief":"a card"}}`); id == "" {
		t.Errorf("admin = %d %s", w.Code, w.Body)
	}
	// No guide: none is pinned.
	id, _ := h.start(h.user, `{"agent":"canvas-chat","input":{"brief":"plain"}}`)
	if run := h.waitDone(id); run.StyleguideID != "" {
		t.Errorf("plain guide = %q", run.StyleguideID)
	}
}
