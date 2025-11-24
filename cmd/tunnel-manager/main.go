package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	"tunnel/internal/client"
	"tunnel/internal/config"
	"tunnel/internal/runtime"
	"tunnel/internal/sync"
)

func main() {
	config, err := config.LoadConfig()
	if err != nil {
		log.Printf("Fatal error: failed to load config: %v\n", err)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: config.LogLevel,
	}))

	config.Print(logger)

	client, err := client.NewClient(config)
	if err != nil {
		fmt.Printf("Fatal error: failed to create clients: %v\n", err)
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	runtime := &runtime.Runtime{
		Ctx:    ctx,
		Config: config,
		Client: client,
		Logger: logger,
	}

	logger.Info("starting tunnel sync loop")
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-time.After(config.SyncInterval):
			logger.Info("sync start")
			if state, err := sync.SyncKube(runtime); err != nil {
				logger.Warn("kubernetes sync failed", slog.String("error", err.Error()))
			} else {
				state.Print(runtime.Logger)
				if err := sync.SyncTunnel(runtime, state); err != nil {
					logger.Warn("tunnel sync failed", slog.String("error", err.Error()))
				}
				if err := sync.SyncDNS(runtime, state); err != nil {
					logger.Warn("dns sync failed", slog.String("error", err.Error()))
				}
			}
			logger.Info("sync stop")
		}
	}
}
