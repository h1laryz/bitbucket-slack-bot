package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bitbucket-slack-bot/internal/bitbucket"
	"bitbucket-slack-bot/internal/config"
	"bitbucket-slack-bot/internal/db"
	slackbot "bitbucket-slack-bot/internal/slack"
	"bitbucket-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	slacklib "github.com/slack-go/slack"
)

// requestLogger returns a Fiber middleware that logs full request and response details.
func requestLogger(log *slog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Snapshot the body before handlers consume it (Fiber body is a byte slice, safe to read).
		body := string(c.Body())

		err := c.Next()

		args := []any{
			"method", c.Method(),
			"url", c.OriginalURL(),
			"status", c.Response().StatusCode(),
			"latency", time.Since(start).String(),
			"ip", c.IP(),
		}
		if body != "" {
			args = append(args, "body", body)
		}
		// Log selected headers that are useful for debugging webhooks.
		for _, h := range []string{
			"Content-Type", "X-Event-Key", "X-Hub-Signature",
			"X-Slack-Signature", "X-Slack-Request-Timestamp",
		} {
			if v := c.Get(h); v != "" {
				args = append(args, h, v)
			}
		}

		log.Info("http", args...)
		return err
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	log.Info("starting", "addr", cfg.ServerAddr)

	// PostgreSQL connection pool.
	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Error("db connect error", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	log.Info("db connected", "max_conns", pool.Config().MaxConns)

	// DB-backed repo subscription + OAuth token store.
	repoStore := store.NewRepoStore(pool)
	if err := repoStore.Migrate(context.Background()); err != nil {
		log.Error("db migrate error", "err", err)
		os.Exit(1)
	}

	// Slack client.
	slackClient := slacklib.New(cfg.SlackBotToken)

	// Bitbucket OAuth handler.
	oauthHandler := bitbucket.NewOAuthHandler(
		cfg.BitbucketClientID,
		cfg.BitbucketClientSecret,
		cfg.PublicURL,
		repoStore,
		slackClient,
		log,
	)

	// refreshFn wraps OAuthHandler.RefreshTokenBg for use in the Slack handler.
	refreshFn := func(rec *store.TokenRecord) (*store.TokenRecord, error) {
		return oauthHandler.RefreshTokenBg(context.Background(), rec)
	}

	// Slack webhook handler.
	slackHandler := slackbot.NewHandler(slackClient, repoStore, oauthHandler.AuthURL, oauthHandler.AuthLoginURL, cfg.PublicURL, log)

	// Fiber app.
	app := fiber.New(fiber.Config{
		AppName:      "bitbucket-slack-bot",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})

	app.Use(cors.New())
	app.Use(recover.New())
	app.Use(requestLogger(log))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	slackbot.RegisterRoutes(app, slackHandler, cfg.SlackSignSecret, refreshFn)
	bitbucket.RegisterRoutes(app,
		bitbucket.NewWebhookHandler(slackClient, repoStore, log),
		oauthHandler,
	)

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
