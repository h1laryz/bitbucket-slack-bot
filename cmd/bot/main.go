package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	botapi "git-slack-bot/internal/api"
	"git-slack-bot/internal/config"
	"git-slack-bot/internal/db"
	slackbot "git-slack-bot/internal/slack"
	"git-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	slacklib "github.com/slack-go/slack"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	log.Info("starting", "git-provider", cfg.GitProvider, "addr", cfg.ServerAddr)

	// PostgreSQL connection pool.
	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Error("db connect error", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	log.Info("db connected", "max_conns", pool.Config().MaxConns)

	// Per-team git credential store.
	teamStore := store.New()

	// Slack client (one per bot instance).
	slackClient := slacklib.New(cfg.SlackBotToken)

	// Slack webhook handler — uses teamStore to look up credentials per request.
	slackHandler := slackbot.NewHandler(slackClient, teamStore, cfg.GitProvider, log)

	// Management API handler — lets admins register team credentials.
	apiHandler := botapi.NewHandler(teamStore)

	// Fiber app.
	app := fiber.New(fiber.Config{
		AppName:      "git-slack-bot",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} ${method} ${path} ${latency}\n",
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "git_provider": cfg.GitProvider})
	})

	slackbot.RegisterRoutes(app, slackHandler, cfg.SlackSignSecret)
	botapi.RegisterRoutes(app, apiHandler, cfg.APIKey)

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("server listening", "addr", cfg.ServerAddr)
		if err := app.Listen(cfg.ServerAddr); err != nil {
			log.Error("server error", "err", err)
		}
	}()

	<-quit
	log.Info("shutting down")
	if err := app.Shutdown(); err != nil {
		log.Error("shutdown error", "err", err)
	}
}
