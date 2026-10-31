package agentkit

import (
	"context"
	"errors"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

type subAgentInput struct {
	Prompt string `json:"prompt" jsonschema:"description=Task or question for the agent"`
}

// AsTool exposes an agent as a tool taking {"prompt": string}. Each call runs in a fresh session
// of a (whose metadata records the parent session, run and tool call when available) and returns
// the final answer text.
func AsTool(a *Agent, name, description string) tool.Tool {
	return tool.New(name, description, func(ctx context.Context, in subAgentInput) (string, error) {
		if in.Prompt == "" {
			return "", errors.New("prompt is required")
		}
		meta := map[string]any{}
		if call, ok := CallFromContext(ctx); ok {
			meta["parent_session_id"] = call.SessionID
			meta["parent_run_id"] = call.RunID
			meta["parent_tool_call_id"] = call.ID
		}
		sess, err := a.NewSession(ctx, SessionOptions{Title: name, Metadata: meta})
		if err != nil {
			return "", err
		}
		res, err := a.RunSync(ctx, sess.ID, UserText(in.Prompt))
		if err != nil {
			return "", err
		}
		return res.Text, nil
	})
}
