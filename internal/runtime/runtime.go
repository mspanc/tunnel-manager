package runtime

import (
	"context"
	"log/slog"
	"tunnel/internal/client"
	"tunnel/internal/config"
)

type Runtime struct {
	Ctx    context.Context
	Config *config.Config
	Client *client.Client
	Logger *slog.Logger
}
