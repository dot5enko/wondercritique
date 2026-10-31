package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
)

// NewServer creates the HTTP server bound to the fx lifecycle. Listening happens in OnStart (bind errors fail
// startup). OnStop shuts down gracefully; past the stop deadline, request contexts (e.g. SSE streams) are
// cancelled and connections closed.
func NewServer(lc fx.Lifecycle, cfg *config.Config, router *chi.Mux, log *slog.Logger) *http.Server {
	base, cancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: it would cut SSE streams. Non-streaming requests are bounded by the Timeout middleware.
		BaseContext: func(net.Listener) context.Context { return base },
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("httpx: listen %s: %w", srv.Addr, err)
			}
			log.Info("http server listening", "addr", ln.Addr().String())
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server stopped", "err", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				cancel()
				return errors.Join(err, srv.Close())
			}
			return nil
		},
	})
	return srv
}
