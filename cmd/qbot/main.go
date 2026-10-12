package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dominatos/qbittorrent-telegram-bot/internal/bot"
	"github.com/dominatos/qbittorrent-telegram-bot/internal/config"
)

func main() {
	versionFlag := flag.Bool("v", false, "Print version and exit")
	versionLongFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *versionFlag || *versionLongFlag {
		fmt.Printf("qBot (qBittorrent Telegram Bot) v%s\n", bot.Version)
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Setup logging
	var logWriter io.Writer = os.Stdout
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err == nil {
			f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				defer f.Close()
				logWriter = io.MultiWriter(os.Stdout, f)
			}
		}
	}

	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	logger.Info("Starting qBittorrent Telegram Bot", "version", bot.Version)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	b, err := bot.NewBot(cfg, logger)
	if err != nil {
		logger.Error("Failed to initialize bot", "err", err)
		os.Exit(1)
	}

	if err := b.Run(ctx); err != nil {
		logger.Error("Bot encountered an error", "err", err)
		os.Exit(1)
	}

	logger.Info("Bot stopped gracefully.")
}
