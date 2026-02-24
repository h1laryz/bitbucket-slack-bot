package provider

import (
	"fmt"
	"time"
)

// Type identifies a git hosting service.
type Type string

const (
	TypeBitbucket Type = "bitbucket"
	TypeGitHub    Type = "github"
)

// TeamConfig holds per-Slack-team credentials for a git provider.
type TeamConfig struct {
	// BaseURL overrides the default API endpoint (optional).
	BaseURL   string `json:"base_url"`
	Workspace string `json:"workspace"`
	Username  string `json:"username"`
	Token     string `json:"token"`
}

// PullRequest is a provider-agnostic representation of a pull request.
type PullRequest struct {
	ID           int
	Title        string
	Description  string
	State        string
	Author       string
	SourceBranch string
	TargetBranch string
	URL          string
	CreatedAt    time.Time
}

// Repository is a provider-agnostic representation of a repository.
type Repository struct {
	Slug        string
	FullName    string
	Description string
	IsPrivate   bool
	URL         string
}

// Provider is the interface every git hosting backend must implement.
type Provider interface {
	ListOpenPRs(repo string) ([]PullRequest, error)
	GetPR(repo string, id int) (*PullRequest, error)
	ListRepos() ([]Repository, error)
}

// New constructs a Provider for the given type and team credentials.
func New(t Type, cfg TeamConfig) (Provider, error) {
	switch t {
	case TypeBitbucket:
		return newBitbucketClient(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported git provider %q", t)
	}
}

// ParseType validates and normalises a provider name string.
func ParseType(s string) (Type, error) {
	switch Type(s) {
	case TypeBitbucket, TypeGitHub:
		return Type(s), nil
	default:
		return "", fmt.Errorf("unknown git provider %q — valid values: bitbucket, github", s)
	}
}
