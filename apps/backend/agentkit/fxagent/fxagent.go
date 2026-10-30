// Package fxagent wires agentkit agents into a go.uber.org/fx application.
package fxagent

import (
	"context"
	"database/sql"

	"github.com/dot5enko/wonder-critique/apps/backend/agentkit"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store"
	"github.com/dot5enko/wonder-critique/apps/backend/agentkit/store/sqlite"
	"go.uber.org/fx"
)

// SQLiteStore provides store.Store backed by an injected *sql.DB (the host
// application owns the connection, e.g. the one behind the app's GORM handle).
func SQLiteStore() fx.Option {
	return fx.Provide(func(db *sql.DB) (store.Store, error) {
		return sqlite.New(db)
	})
}

// Provide registers an *agentkit.Agent constructor. The constructor's result
// is published as `name:"<name>"` so multiple agents can coexist; consume one
// with fx.Annotate(f, fx.ParamTags(`name:"critic"`)). On start the agent
// recovers runs that were left running by a previous process.
func Provide(name string, ctor any) fx.Option {
	return fx.Options(
		fx.Provide(fx.Annotate(ctor, fx.ResultTags(`name:"`+name+`"`))),
		fx.Invoke(fx.Annotate(func(a *agentkit.Agent, lc fx.Lifecycle) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					_, err := a.Recover(ctx)
					return err
				},
			})
		}, fx.ParamTags(`name:"`+name+`"`))),
	)
}
