// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"go.uber.org/fx"
)

const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Config is the application configuration, populated from environment variables.
type Config struct {
	Env         string     `env:"APP_ENV" envDefault:"dev"`
	LogLevel    slog.Level `env:"LOG_LEVEL" envDefault:"info"`
	HTTPAddr    string     `env:"HTTP_ADDR" envDefault:":8080"`
	CORSOrigins []string   `env:"CORS_ORIGINS" envSeparator:","`
	DBPath      string     `env:"DB_PATH" envDefault:"data/app.db"`

	JWTSecret    string `env:"JWT_SECRET"`
	JWTIssuer    string `env:"JWT_ISSUER" envDefault:"wonder-critique"`
	ResendAPIKey string `env:"RESEND_API_KEY"`
	MailFrom     string `env:"MAIL_FROM"`
	// AdminEmails are upserted as admin users at startup (created if missing, promoted if present).
	AdminEmails []string `env:"ADMIN_EMAILS" envSeparator:","`

	// Legacy Meta key used by the pre-studio demo critic; kept as a key
	// fallback for the `meta` provider (models.dev env name: META_MODEL_API_KEY).
	ModelAPIKey string `env:"MODEL_API_KEY"`
	AgentModel  string `env:"AGENT_MODEL" envDefault:"muse-spark-1.3-contributor"`

	// SecretsKey encrypts stored provider API keys (AES-GCM). Required in prod
	// (>= 32 bytes); in dev an empty value derives a stable key from JWT_SECRET.
	SecretsKey string `env:"SECRETS_KEY"`

	// Render (see docs/backend.render.md). Chrome is only started on the
	// first render, so none of this is required to boot the app.
	RenderChromePath      string        `env:"RENDER_CHROME_PATH"` // empty → auto-detect
	RenderConcurrency     int           `env:"RENDER_CONCURRENCY" envDefault:"2"`
	RenderTimeout         time.Duration `env:"RENDER_TIMEOUT" envDefault:"20s"`
	RenderAllowedHosts    []string      `env:"RENDER_ALLOWED_HOSTS" envSeparator:","`
	RenderChromeNoSandbox bool          `env:"RENDER_CHROME_NO_SANDBOX" envDefault:"false"`
}

// Module provides *Config.
var Module = fx.Module("config", fx.Provide(New))

// New loads an optional .env file from the working directory, then parses the environment.
// Variables already set in the environment take precedence over .env.
func New() (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("config: load .env: %w", err)
	}
	var c Config
	if err := env.Parse(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// IsDev reports whether the app runs in development mode.
func (c *Config) IsDev() bool { return c.Env == EnvDev }

// Validate checks invariants that cannot be expressed with struct tags.
func (c *Config) Validate() error {
	switch c.Env {
	case EnvDev:
	case EnvProd:
		if len(c.JWTSecret) < 32 {
			return errors.New("config: JWT_SECRET must be at least 32 bytes in prod")
		}
	default:
		return fmt.Errorf("config: APP_ENV must be %q or %q, got %q", EnvDev, EnvProd, c.Env)
	}
	return nil
}
