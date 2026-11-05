package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
)

// A chat with a style guide: the chat agent and its gen_components workers
// get the guide in the system prompt and at /styleguide/STYLEGUIDE.md.
func TestStyleguideReachesSystemPromptMountAndWorkers(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	if err := h.db.AutoMigrate(&models.Styleguide{}); err != nil {
		t.Fatal(err)
	}
	guide := "# Acme style guide\n\nPrimary #ff5500, radius 12px."
	sg := models.Styleguide{Base: models.Base{ID: models.NewID()}, OwnerID: "user1", Name: "Acme", Markdown: guide, Status: models.StyleguideReady}
	if err := h.db.Create(&sg).Error; err != nil {
		t.Fatal(err)
	}

	ms := h.seedModelSet()
	worker := h.seedAgent("gen", ms.ID, "artifact", true)
	body := studio.AgentBody{
		ModelSetID: ms.ID, SystemPrompt: "chat", InputTemplate: "{{brief}}",
		Tools:          []string{"gen_components"},
		OutputContract: studio.OutputContract{Type: "chat"},
		ToolConfig:     &studio.ToolConfig{GenComponents: &studio.GenerateToolConfig{AgentID: worker.ID}},
	}
	raw, _ := json.Marshal(body)
	chat, _ := h.store.Create(ctx, studio.KindAgent, studio.DefIdentity{Slug: "chat", Name: "chat"}, raw)
	if _, _, err := h.store.Publish(ctx, studio.KindAgent, chat.ID, "", "t", raw); err != nil {
		t.Fatal(err)
	}
	h.prov.Push(
		fake.ToolCalls(fake.Call("g1", "gen_components", map[string]any{"description": "a button"})),
		fake.ToolCalls(fake.Call("w1", "submit_artifact", map[string]string{"html": "<button class='b'>go</button>"})),
		fake.Text("Done."),
	)
	res, err := h.runtime.RunNode(WithSink(ctx, &recSink{}), NodeRequest{
		AgentID: chat.ID, Samples: map[string]any{"brief": "a button"}, Owner: "user1", StyleguideID: sg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	reqs := h.prov.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for i, r := range reqs { // chat, worker, chat again
		if !strings.HasPrefix(r.System, "chat") && i != 1 {
			t.Errorf("request %d system lost its prompt: %q", i, r.System)
		}
		if !strings.Contains(r.System, "## Style guide") || !strings.Contains(r.System, "Primary #ff5500") {
			t.Errorf("request %d system has no style guide: %q", i, r.System)
		}
	}

	sess, err := h.runtime.agentStore.GetSession(ctx, res.SessionID)
	if err != nil || sess.Metadata["styleguide_id"] != sg.ID {
		t.Fatalf("session metadata = %v, %v", sess, err)
	}
	mnt, ok, err := h.runtime.sessionMount(ctx, res.SessionID)
	if err != nil || !ok {
		t.Fatalf("mount: %v %v", ok, err)
	}
	if got, err := mnt.Read(ctx, "/styleguide/STYLEGUIDE.md"); err != nil || string(got) != guide {
		t.Errorf("read guide = %q, %v", got, err)
	}
	if err := mnt.Write(ctx, "/styleguide/STYLEGUIDE.md", []byte("x")); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("write guide: %v", err)
	}

	// A deleted guide still applies to the chats that picked it.
	if err := h.db.Delete(&sg).Error; err != nil {
		t.Fatal(err)
	}
	if md, err := h.runtime.styleguideMarkdown(ctx, sg.ID); err != nil || md != guide {
		t.Errorf("deleted guide = %q, %v", md, err)
	}
}

func TestStyleguideSystem(t *testing.T) {
	if got := StyleguideSystem("sys", ""); got != "sys" {
		t.Errorf("empty guide changed the prompt: %q", got)
	}
	got := StyleguideSystem("sys\n", "# G")
	if !strings.HasPrefix(got, "sys\n\n## Style guide\n\n") || !strings.HasSuffix(got, "<styleguide>\n# G\n</styleguide>") {
		t.Errorf("prompt = %q", got)
	}
	long := StyleguideSystem("", strings.Repeat("x", maxStyleguidePrompt+10))
	if !strings.Contains(long, "[… cut; read the full guide at /styleguide/STYLEGUIDE.md]") {
		t.Error("long guide not cut")
	}
}
