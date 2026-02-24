package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bitbucket-slack-bot/internal/bitbucket"
	"bitbucket-slack-bot/internal/config"
	slackbot "bitbucket-slack-bot/internal/slack"

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
		log.Error("config load failed", "err", err)
		os.Exit(1)
	}

	// Clients
	slackClient := slacklib.New(cfg.SlackBotToken)
	bbClient := bitbucket.NewClient(
		cfg.BitbucketBaseURL,
		cfg.BitbucketWorkspace,
		cfg.BitbucketUsername,
		cfg.BitbucketToken,
	)

	// Slack handler
	handler := slackbot.NewHandler(slackClient, bbClient, log)

	// Fiber app
	app := fiber.New(fiber.Config{
		AppName:      "bitbucket-slack-bot",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} ${method} ${path} ${latency}\n",
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	slackbot.RegisterRoutes(app, handler, cfg.SlackSignSecret)

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("server starting", "addr", cfg.ServerAddr)
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
