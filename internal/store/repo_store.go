package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RepoStore persists channel→repo subscriptions and Bitbucket OAuth tokens in PostgreSQL.
type RepoStore struct {
	pool *pgxpool.Pool
}

func NewRepoStore(pool *pgxpool.Pool) *RepoStore {
	return &RepoStore{pool: pool}
}

// Migrate creates all required tables if they do not already exist.
func (s *RepoStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS repo_subscriptions (
			id         SERIAL PRIMARY KEY,
			channel_id TEXT        NOT NULL,
			team_id    TEXT        NOT NULL,
			repo_slug  TEXT        NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(channel_id, repo_slug)
		);

		CREATE TABLE IF NOT EXISTS bitbucket_tokens (
			team_id       TEXT PRIMARY KEY,
			workspace     TEXT        NOT NULL,
			access_token  TEXT        NOT NULL,
			refresh_token TEXT        NOT NULL,
			expires_at    TIMESTAMPTZ NOT NULL,
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS webhook_secrets (
			repo_slug  TEXT PRIMARY KEY,
			secret     TEXT        NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS pr_messages (
			repo_slug   TEXT    NOT NULL,
			pr_id       INTEGER NOT NULL,
			channel_id  TEXT    NOT NULL,
			message_ts  TEXT    NOT NULL,
			PRIMARY KEY (repo_slug, pr_id, channel_id)
		);

		CREATE TABLE IF NOT EXISTS pr_approvals (
			repo_slug   TEXT    NOT NULL,
			pr_id       INTEGER NOT NULL,
			user_name   TEXT    NOT NULL,
			PRIMARY KEY (repo_slug, pr_id, user_name)
		);

		CREATE TABLE IF NOT EXISTS user_mappings (
			slack_user_id      TEXT PRIMARY KEY,
			bitbucket_username TEXT NOT NULL UNIQUE,
			created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS pr_commits (
			repo_slug      TEXT    NOT NULL,
			pr_id          INTEGER NOT NULL,
			commit_hash    TEXT    NOT NULL,
			pr_title       TEXT    NOT NULL,
			pr_url         TEXT    NOT NULL,
			author_name    TEXT    NOT NULL,
			reviewer_names TEXT    NOT NULL DEFAULT '[]',
			source_branch  TEXT    NOT NULL,
			dest_branch    TEXT    NOT NULL,
			PRIMARY KEY (repo_slug, pr_id)
		);
		CREATE INDEX IF NOT EXISTS idx_pr_commits_hash ON pr_commits (repo_slug, commit_hash);

		CREATE TABLE IF NOT EXISTS build_statuses (
			repo_slug   TEXT        NOT NULL,
			commit_hash TEXT        NOT NULL,
			state       TEXT        NOT NULL,
			name        TEXT        NOT NULL,
			url         TEXT        NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (repo_slug, commit_hash)
		);
	`)
	return err
}

// Subscribe registers channel to receive PR notifications for repoSlug.
// Duplicate subscriptions are silently ignored.
func (s *RepoStore) Subscribe(ctx context.Context, channelID, teamID, repoSlug string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO repo_subscriptions (channel_id, team_id, repo_slug)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		channelID, teamID, repoSlug,
	)
	return err
}

// Unsubscribe removes a channel's subscription to repoSlug.
func (s *RepoStore) Unsubscribe(ctx context.Context, channelID, repoSlug string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM repo_subscriptions WHERE channel_id = $1 AND repo_slug = $2`,
		channelID, repoSlug,
	)
	return err
}

// ChannelsForRepo returns all channel IDs subscribed to repoSlug.
func (s *RepoStore) ChannelsForRepo(ctx context.Context, repoSlug string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT channel_id FROM repo_subscriptions WHERE repo_slug = $1`,
		repoSlug,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// ListForChannel returns all repo slugs subscribed in channelID, ordered by subscription time.
func (s *RepoStore) ListForChannel(ctx context.Context, channelID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT repo_slug FROM repo_subscriptions WHERE channel_id = $1 ORDER BY created_at`,
		channelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// TokenRecord holds OAuth tokens for a Slack team.
type TokenRecord struct {
	TeamID       string
	Workspace    string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// SaveToken stores or updates OAuth tokens for a team.
func (s *RepoStore) SaveToken(ctx context.Context, teamID, workspace, accessToken, refreshToken string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO bitbucket_tokens (team_id, workspace, access_token, refresh_token, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (team_id) DO UPDATE SET
			workspace     = EXCLUDED.workspace,
			access_token  = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			expires_at    = EXCLUDED.expires_at,
			updated_at    = NOW()
	`, teamID, workspace, accessToken, refreshToken, expiresAt)
	return err
}

// GetToken retrieves OAuth tokens for a team. Returns pgx.ErrNoRows if not found.
func (s *RepoStore) GetToken(ctx context.Context, teamID string) (*TokenRecord, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT team_id, workspace, access_token, refresh_token, expires_at
		 FROM bitbucket_tokens WHERE team_id = $1`,
		teamID,
	)
	var t TokenRecord
	if err := row.Scan(&t.TeamID, &t.Workspace, &t.AccessToken, &t.RefreshToken, &t.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// DeleteToken removes OAuth tokens for a team.
func (s *RepoStore) DeleteToken(ctx context.Context, teamID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM bitbucket_tokens WHERE team_id = $1`, teamID)
	return err
}

// PRMessage holds the Slack channel and message timestamp for a PR notification.
type PRMessage struct {
	ChannelID string
	MessageTS string
}

// SavePRMessage stores (or replaces) the Slack message ts for a PR in a channel.
func (s *RepoStore) SavePRMessage(ctx context.Context, repoSlug string, prID int, channelID, messageTS string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pr_messages (repo_slug, pr_id, channel_id, message_ts)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (repo_slug, pr_id, channel_id) DO UPDATE SET message_ts = EXCLUDED.message_ts
	`, repoSlug, prID, channelID, messageTS)
	return err
}

// GetPRMessages returns all channel+ts pairs for a PR (used to thread follow-up events).
func (s *RepoStore) GetPRMessages(ctx context.Context, repoSlug string, prID int) ([]PRMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT channel_id, message_ts FROM pr_messages WHERE repo_slug = $1 AND pr_id = $2`,
		repoSlug, prID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []PRMessage
	for rows.Next() {
		var m PRMessage
		if err := rows.Scan(&m.ChannelID, &m.MessageTS); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetOrCreateWebhookSecret returns the existing webhook secret for repoSlug,
// or generates and stores a new one if none exists.
func (s *RepoStore) GetOrCreateWebhookSecret(ctx context.Context, repoSlug string) (string, error) {
	// Try to get existing secret first.
	secret, err := s.GetWebhookSecret(ctx, repoSlug)
	if err != nil {
		return "", err
	}
	if secret != "" {
		return secret, nil
	}

	// Generate a new 32-byte random secret.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret = hex.EncodeToString(b)

	_, err = s.pool.Exec(ctx,
		`INSERT INTO webhook_secrets (repo_slug, secret) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		repoSlug, secret,
	)
	if err != nil {
		return "", err
	}
	return secret, nil
}

// GetWebhookSecret returns the webhook secret for repoSlug, or "" if not set.
func (s *RepoStore) GetWebhookSecret(ctx context.Context, repoSlug string) (string, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT secret FROM webhook_secrets WHERE repo_slug = $1`,
		repoSlug,
	)
	var secret string
	if err := row.Scan(&secret); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return secret, nil
}

// AddApproval records an approval for a PR by userName. Duplicate approvals are ignored.
func (s *RepoStore) AddApproval(ctx context.Context, repoSlug string, prID int, userName string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO pr_approvals (repo_slug, pr_id, user_name) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		repoSlug, prID, userName,
	)
	return err
}

// RemoveApproval deletes an approval for a PR by userName.
func (s *RepoStore) RemoveApproval(ctx context.Context, repoSlug string, prID int, userName string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM pr_approvals WHERE repo_slug = $1 AND pr_id = $2 AND user_name = $3`,
		repoSlug, prID, userName,
	)
	return err
}

// GetApprovals returns all approver names for a PR, ordered by insertion time.
func (s *RepoStore) GetApprovals(ctx context.Context, repoSlug string, prID int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_name FROM pr_approvals WHERE repo_slug = $1 AND pr_id = $2 ORDER BY ctid`,
		repoSlug, prID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// SaveUserMapping stores or updates the link between a Slack user and their Bitbucket display name.
func (s *RepoStore) SaveUserMapping(ctx context.Context, slackUserID, bitbucketUsername string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_mappings (slack_user_id, bitbucket_username)
		VALUES ($1, $2)
		ON CONFLICT (slack_user_id) DO UPDATE SET bitbucket_username = EXCLUDED.bitbucket_username
	`, slackUserID, bitbucketUsername)
	return err
}

// GetSlackUserByBitbucket returns the Slack user ID linked to a Bitbucket display name,
// or "" if no mapping exists.
func (s *RepoStore) GetSlackUserByBitbucket(ctx context.Context, bitbucketUsername string) (string, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT slack_user_id FROM user_mappings WHERE bitbucket_username = $1`,
		bitbucketUsername,
	)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

// PRCommitRecord stores the PR info needed to rebuild Slack cards on pipeline status changes.
type PRCommitRecord struct {
	RepoSlug      string
	PRID          int
	CommitHash    string
	Title         string
	URL           string
	AuthorName    string
	ReviewerNames []string
	SourceBranch  string
	DestBranch    string
}

// SavePRCommit upserts the PR info and source commit hash.
func (s *RepoStore) SavePRCommit(ctx context.Context, rec PRCommitRecord) error {
	reviewersJSON, _ := json.Marshal(rec.ReviewerNames)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pr_commits (repo_slug, pr_id, commit_hash, pr_title, pr_url, author_name, reviewer_names, source_branch, dest_branch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (repo_slug, pr_id) DO UPDATE SET
			commit_hash    = EXCLUDED.commit_hash,
			pr_title       = EXCLUDED.pr_title,
			pr_url         = EXCLUDED.pr_url,
			author_name    = EXCLUDED.author_name,
			reviewer_names = EXCLUDED.reviewer_names,
			source_branch  = EXCLUDED.source_branch,
			dest_branch    = EXCLUDED.dest_branch
	`, rec.RepoSlug, rec.PRID, rec.CommitHash, rec.Title, rec.URL,
		rec.AuthorName, string(reviewersJSON), rec.SourceBranch, rec.DestBranch)
	return err
}

// GetPRCommit retrieves the cached PR info. Returns nil if not found.
func (s *RepoStore) GetPRCommit(ctx context.Context, repoSlug string, prID int) (*PRCommitRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT repo_slug, pr_id, commit_hash, pr_title, pr_url, author_name, reviewer_names, source_branch, dest_branch
		FROM pr_commits WHERE repo_slug = $1 AND pr_id = $2
	`, repoSlug, prID)
	var rec PRCommitRecord
	var reviewersJSON string
	if err := row.Scan(&rec.RepoSlug, &rec.PRID, &rec.CommitHash, &rec.Title, &rec.URL,
		&rec.AuthorName, &reviewersJSON, &rec.SourceBranch, &rec.DestBranch); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	json.Unmarshal([]byte(reviewersJSON), &rec.ReviewerNames)
	return &rec, nil
}

// GetPRsByCommit returns all PR IDs whose source commit matches the given hash.
func (s *RepoStore) GetPRsByCommit(ctx context.Context, repoSlug, commitHash string) ([]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT pr_id FROM pr_commits WHERE repo_slug = $1 AND commit_hash = $2`,
		repoSlug, commitHash,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// BuildStatus holds the latest pipeline/build status for a commit.
type BuildStatus struct {
	State string
	Name  string
	URL   string
}

// SaveBuildStatus upserts the latest build status for a commit.
func (s *RepoStore) SaveBuildStatus(ctx context.Context, repoSlug, commitHash, state, name, buildURL string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO build_statuses (repo_slug, commit_hash, state, name, url, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (repo_slug, commit_hash) DO UPDATE SET
			state      = EXCLUDED.state,
			name       = EXCLUDED.name,
			url        = EXCLUDED.url,
			updated_at = NOW()
	`, repoSlug, commitHash, state, name, buildURL)
	return err
}

// GetBuildStatus retrieves the latest build status for a commit. Returns nil if not found.
func (s *RepoStore) GetBuildStatus(ctx context.Context, repoSlug, commitHash string) (*BuildStatus, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT state, name, url FROM build_statuses WHERE repo_slug = $1 AND commit_hash = $2`,
		repoSlug, commitHash,
	)
	var bs BuildStatus
	if err := row.Scan(&bs.State, &bs.Name, &bs.URL); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &bs, nil
}
