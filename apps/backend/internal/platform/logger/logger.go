// Package logger provides the application *slog.Logger.
package logger

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
)

// Module provides *slog.Logger and routes fx's own events to it at debug level.
// WithLogger stays outside fx.Module, where it would only apply to the module's own scope.
var Module = fx.Options(
	fx.Module("logger", fx.Provide(New)),
	fx.WithLogger(func(l *slog.Logger) fxevent.Logger {
		fl := &fxevent.SlogLogger{Logger: l.With("component", "fx")}
		fl.UseLogLevel(slog.LevelDebug)
		return fl
	}),
)

// New builds a text logger in dev and a JSON logger otherwise, and installs it as slog's default.
func New(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.IsDev() {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
