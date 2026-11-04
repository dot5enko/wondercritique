// The built-in design-chat agent: the canvas chat talks to it directly (no
// pipeline); it creates components with gen_components.
package studio

import (
	"context"
	"encoding/json"
	"log/slog"
)

const (
	chatAgentName        = "Canvas chat"
	chatAgentDescription = "The design chat: talks with the user about the canvas and creates components with gen_components."
)

const chatAgentSystem = `You are the design assistant of a canvas chat. The user and you share a
canvas of HTML components (files in folders; folders = groups). You talk with
the user and create components for them.

Creating components:
- Call gen_components(count, description) to create components. count is how
  many separate components (or variants) to create, default 1. description is
  a complete, self-contained brief for the generator: what to build, the
  content, the style, constraints from the conversation. The generator does
  not see this chat, so put everything it needs into the description.
- When the user asks for N variants or N components, call gen_components
  with count N. Every generated component stays on the canvas: never reduce
  them to one and never pick a "best" one unless the user asks for that.
- To give each variant a direction, pass variations (one hint per component).
- To change or build on existing components, pass their canvas paths as
  sources; they are never modified, the result is new files.
- Different kinds of components in one answer (e.g. a header and a footer)
  are separate gen_components calls.
- Pass folder (a short name for the new group) and names (one file name per
  component) when you can name them well.

Talking:
- When the user only asks a question or wants feedback, answer without
  generating anything. You can read the canvas files under /canvas.
- After generating, reply briefly: what you created and how they differ.
  Don't paste the HTML into your reply.`

func chatAgentSeed(modelSetID string) AgentBody {
	return AgentBody{
		ModelSetID:     modelSetID,
		SystemPrompt:   chatAgentSystem,
		InputTemplate:  "{{history}}\n\n{{canvas}}\n\n## Message\n\n{{brief}}",
		Tools:          []string{"read_file", "glob", "grep", "gen_components"},
		OutputContract: OutputContract{Type: "chat"},
	}
}

// ensureChatAgent adds the canvas-chat agent when it is missing (fresh seeds
// and installs that predate it; uses the "default" model set, skipped when
// that is gone). Deleting canvas-chat re-creates it on the next start.
func ensureChatAgent(ctx context.Context, store *Store, log *slog.Logger) error {
	if _, err := store.GetBySlug(ctx, KindAgent, ChatAgentSlug); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	ms, err := store.GetBySlug(ctx, KindModelSet, "default")
	if err != nil {
		log.Info("studio: canvas-chat not seeded (no default model set)")
		return nil
	}
	raw, err := json.Marshal(chatAgentSeed(ms.ID))
	if err != nil {
		return err
	}
	def, err := store.Create(ctx, KindAgent, DefIdentity{Slug: ChatAgentSlug, Name: chatAgentName, Description: chatAgentDescription}, raw)
	if err != nil {
		return err
	}
	if _, _, err := store.Publish(ctx, KindAgent, def.ID, "seeded", "system", raw); err != nil {
		return err
	}
	log.Info("studio: seeded canvas-chat agent")
	return nil
}
