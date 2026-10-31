package agentkit

import (
	"context"
	"slices"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
)

// ContextPolicy decides which history is sent to the model on each step. It must not mutate history.
type ContextPolicy interface {
	Messages(ctx context.Context, sess *store.Session, history []llm.Message) ([]llm.Message, error)
}

// ContextPolicyFunc adapts a function to ContextPolicy.
type ContextPolicyFunc func(ctx context.Context, sess *store.Session, history []llm.Message) ([]llm.Message, error)

// Messages implements ContextPolicy.
func (f ContextPolicyFunc) Messages(ctx context.Context, sess *store.Session, history []llm.Message) ([]llm.Message, error) {
	return f(ctx, sess, history)
}

// PassThrough sends the full history.
func PassThrough() ContextPolicy {
	return ContextPolicyFunc(func(_ context.Context, _ *store.Session, h []llm.Message) ([]llm.Message, error) {
		return h, nil
	})
}

// DropSettledReasoning sends the full history while its approximate size (bytes of text, inputs,
// outputs and opaque payloads) is within budget; above it, reasoning blocks of turns before the
// last user message are dropped.
func DropSettledReasoning(budget int) ContextPolicy {
	return ContextPolicyFunc(func(_ context.Context, _ *store.Session, h []llm.Message) ([]llm.Message, error) {
		if size(h) <= budget {
			return h, nil
		}
		lastUser := -1
		for i, m := range h {
			if m.Role == llm.RoleUser {
				lastUser = i
			}
		}
		out := make([]llm.Message, 0, len(h))
		for i, m := range h {
			if i < lastUser && m.Role == llm.RoleAssistant {
				m.Blocks = slices.DeleteFunc(slices.Clone(m.Blocks), func(b llm.Block) bool { return b.Type == llm.BlockReasoning })
				if len(m.Blocks) == 0 {
					continue
				}
			}
			out = append(out, m)
		}
		return out, nil
	})
}

func size(h []llm.Message) int {
	n := 0
	for _, m := range h {
		for _, b := range m.Blocks {
			n += len(b.Text) + len(b.Input) + len(b.Output) + len(b.Opaque) + len(b.ImageURL)
		}
	}
	return n
}
