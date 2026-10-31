//go:build live

// Live test against the real Meta Model API. Run with:
//
//	MODEL_API_KEY=... go test -tags live -run TestLive ./llm/meta/
package meta_test

import (
	"context"
	"os"
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/meta"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
)

func TestLive(t *testing.T) {
	key := os.Getenv("MODEL_API_KEY")
	if key == "" {
		t.Skip("MODEL_API_KEY not set")
	}
	a, err := agentkit.New(agentkit.Config{
		Name:     "live",
		Provider: meta.New(meta.Config{APIKey: key}),
		Store:    memory.New(),
		Tools: tool.NewSet(tool.New("echo", "echoes its input", func(ctx context.Context, in struct {
			Text string `json:"text"`
		}) (string, error) {
			return in.Text, nil
		})),
		MaxSteps: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.NewSession(context.Background(), agentkit.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.RunSync(context.Background(), sess.ID,
		agentkit.UserText(`Use the echo tool with text "hello", then tell me exactly what it returned.`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("answer: %s\nusage: %+v", res.Text, res.Usage)
}
