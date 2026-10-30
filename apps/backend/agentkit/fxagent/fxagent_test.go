package fxagent_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/fxagent"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/llm/fake"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	_ "modernc.org/sqlite"
)

func TestWiring(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var ran bool
	app := fxtest.New(t,
		fx.Provide(func() *sql.DB { return db }),
		fxagent.SQLiteStore(),
		fxagent.Provide("test", func(s store.Store) (*agentkit.Agent, error) {
			return agentkit.New(agentkit.Config{
				Name: "test", Provider: fake.New(fake.Text("hi")), Store: s,
			})
		}),
		fx.Invoke(fx.Annotate(func(a *agentkit.Agent) {
			if a.Name() != "test" {
				t.Errorf("agent name = %q", a.Name())
			}
			ran = true
		}, fx.ParamTags(`name:"test"`))),
	)
	app.RequireStart().RequireStop()
	if !ran {
		t.Fatal("agent was not consumed")
	}
}
