package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"discord-music-bot/internal/bot"
	"discord-music-bot/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := bot.New(cfg, logger)
	if err != nil {
		logger.Error("create bot", "error", err)
		os.Exit(1)
	}

	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("run bot", "error", err)
		os.Exit(1)
	}
}
