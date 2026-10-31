package main

import (
	"go.uber.org/fx"

	"github.com/dot5enko/wondercritique/apps/backend/internal/app"
)

func main() {
	fx.New(app.Module).Run()
}
