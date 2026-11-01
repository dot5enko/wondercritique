package render

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
)

// Module provides the Renderer (interface) and the concrete *Chrome.
// Construction is side-effect free: Chrome, the egress proxy and the browser
// path lookup all happen lazily on the first Render, and the browser is
// stopped on app shutdown.
var Module = fx.Module("render",
	fx.Provide(newChrome),
	fx.Provide(func(c *Chrome) Renderer { return c }),
)

func newChrome(lc fx.Lifecycle, cfg *config.Config, log *slog.Logger) (*Chrome, error) {
	r, err := New(Config{
		ChromePath:   cfg.RenderChromePath,
		Concurrency:  cfg.RenderConcurrency,
		Timeout:      cfg.RenderTimeout,
		AllowedHosts: cfg.RenderAllowedHosts,
		NoSandbox:    cfg.RenderChromeNoSandbox,
		Logger:       log,
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StopHook(func() error { return r.Close() }))
	return r, nil
}
