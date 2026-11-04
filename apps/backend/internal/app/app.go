// Package app is the composition root.
package app

import (
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
	"github.com/dot5enko/wondercritique/apps/backend/internal/crud"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/canvas"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/engine"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/health"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/messages"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/render"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/logger"
)

// Module wires the whole application.
var Module = fx.Options(
	// platform
	config.Module,
	logger.Module,
	dbx.Module,
	httpx.Module,

	// feature modules (append new features here)
	health.Module,
	auth.Module,
	crud.Module,
	messages.Module,
	llm.Module,
	studio.Module,
	canvas.Module,
	agentrun.Module,
	engine.Module,
	render.Module,
	blobs.Module,
)
