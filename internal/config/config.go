package config

import (
	"flag"
	"fmt"
	"strings"

	"git-slack-bot/internal/provider"
)

// Config holds all runtime configuration sourced from CLI flags.
// Git provider credentials are NOT here — they are set per Slack team
// via POST /api/teams/{team_id}/config.
type Config struct {
	// ServerAddr is the address the HTTP server listens on (e.g. ":3000").
	ServerAddr string

	// Slack application credentials.
	SlackBotToken   string
	SlackSignSecret string

	// GitProvider is the git hosting backend to use (bitbucket, github).
	GitProvider provider.Type

	// APIKey protects the /api/teams/* management endpoints.
	APIKey string

	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string
}

func Load() (*Config, error) {
	cfg := &Config{}
	var gitProvider string

	flag.StringVar(&cfg.ServerAddr, "addr", ":3000", "address the server listens on")
	flag.StringVar(&cfg.SlackBotToken, "slack-bot-token", "", "Slack bot token (xoxb-…)")
	flag.StringVar(&cfg.SlackSignSecret, "slack-signing-secret", "", "Slack signing secret")
	flag.StringVar(&gitProvider, "git-provider", "", "git hosting provider: bitbucket, github")
	flag.StringVar(&cfg.APIKey, "api-key", "", "bearer token protecting the /api/teams/* endpoints")
	flag.StringVar(&cfg.DatabaseURL, "db-url", "", "PostgreSQL connection URL (postgres://user:pass@host/db)")
	flag.Parse()

	if err := cfg.validate(gitProvider); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate(gitProvider string) error {
	var missing []string

	if c.SlackBotToken == "" {
		missing = append(missing, "--slack-bot-token")
	}
	if c.SlackSignSecret == "" {
		missing = append(missing, "--slack-signing-secret")
	}
	if c.APIKey == "" {
		missing = append(missing, "--api-key")
	}

	if c.DatabaseURL == "" {
		missing = append(missing, "--db-url")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}

	pt, err := provider.ParseType(gitProvider)
	if err != nil {
		return fmt.Errorf("--git-provider: %w", err)
	}
	c.GitProvider = pt

	return nil
}
