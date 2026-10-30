// Package app is the composition root.
package app

import (
	"go.uber.org/fx"

	"github.com/dot5enko/wonder-critique/apps/backend/internal/config"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/crud"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/agents"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/auth"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/health"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/features/messages"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/httpx"
	"github.com/dot5enko/wonder-critique/apps/backend/internal/platform/logger"
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
	agents.Module,
)
