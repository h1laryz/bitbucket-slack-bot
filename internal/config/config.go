package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	ServerAddr string

	// Slack
	SlackBotToken   string
	SlackSignSecret string

	// Bitbucket
	BitbucketBaseURL   string
	BitbucketUsername  string
	BitbucketToken     string
	BitbucketWorkspace string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		ServerAddr:         getEnvOrDefault("SERVER_ADDR", ":3000"),
		SlackBotToken:      os.Getenv("SLACK_BOT_TOKEN"),
		SlackSignSecret:    os.Getenv("SLACK_SIGNING_SECRET"),
		BitbucketBaseURL:   getEnvOrDefault("BITBUCKET_BASE_URL", "https://api.bitbucket.org/2.0"),
		BitbucketUsername:  os.Getenv("BITBUCKET_USERNAME"),
		BitbucketToken:     os.Getenv("BITBUCKET_TOKEN"),
		BitbucketWorkspace: os.Getenv("BITBUCKET_WORKSPACE"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	required := map[string]string{
		"SLACK_BOT_TOKEN":      c.SlackBotToken,
		"SLACK_SIGNING_SECRET": c.SlackSignSecret,
		"BITBUCKET_USERNAME":   c.BitbucketUsername,
		"BITBUCKET_TOKEN":      c.BitbucketToken,
		"BITBUCKET_WORKSPACE":  c.BitbucketWorkspace,
	}

	for key, val := range required {
		if val == "" {
			return fmt.Errorf("missing required env var: %s", key)
		}
	}

	return nil
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
