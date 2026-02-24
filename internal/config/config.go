package config

import (
	"flag"
	"fmt"
	"strings"
)

// Config holds all runtime configuration sourced from CLI flags.
type Config struct {
	// ServerAddr is the address the HTTP server listens on (e.g. ":3000").
	ServerAddr string

	// Slack application credentials.
	SlackBotToken   string
	SlackSignSecret string

	// Bitbucket OAuth2 consumer credentials.
	BitbucketClientID     string
	BitbucketClientSecret string

	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string

	// PublicURL is the externally reachable base URL of the bot (e.g. https://abc.ngrok-free.app).
	PublicURL string
}

func Load() (*Config, error) {
	cfg := &Config{}

	flag.StringVar(&cfg.ServerAddr, "addr", ":3000", "address the server listens on")
	flag.StringVar(&cfg.SlackBotToken, "slack-bot-token", "", "Slack bot token (xoxb-…)")
	flag.StringVar(&cfg.SlackSignSecret, "slack-signing-secret", "", "Slack signing secret")
	flag.StringVar(&cfg.BitbucketClientID, "bitbucket-client-id", "", "Bitbucket OAuth2 consumer client ID")
	flag.StringVar(&cfg.BitbucketClientSecret, "bitbucket-client-secret", "", "Bitbucket OAuth2 consumer client secret")
	flag.StringVar(&cfg.DatabaseURL, "db-url", "", "PostgreSQL connection URL (postgres://user:pass@host/db)")
	flag.StringVar(&cfg.PublicURL, "public-url", "", "externally reachable base URL (e.g. https://abc.ngrok-free.app)")
	flag.Parse()

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string

	if c.SlackBotToken == "" {
		missing = append(missing, "--slack-bot-token")
	}
	if c.SlackSignSecret == "" {
		missing = append(missing, "--slack-signing-secret")
	}
	if c.BitbucketClientID == "" {
		missing = append(missing, "--bitbucket-client-id")
	}
	if c.BitbucketClientSecret == "" {
		missing = append(missing, "--bitbucket-client-secret")
	}
	if c.DatabaseURL == "" {
		missing = append(missing, "--db-url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}

	return nil
}
