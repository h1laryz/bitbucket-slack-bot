package provider

import "time"

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
