package slack

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"git-slack-bot/internal/provider"
	"git-slack-bot/internal/store"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// Handler processes Slack events and slash commands.
type Handler struct {
	client    *slack.Client
	repoStore *store.RepoStore
	oauthURL  func(teamID, channelID, workspace string) string
	loginURL  func(slackUserID, channelID string) string
	publicURL string
	log       *slog.Logger
}

func NewHandler(client *slack.Client, repoStore *store.RepoStore, oauthURL func(teamID, channelID, workspace string) string, loginURL func(slackUserID, channelID string) string, publicURL string, log *slog.Logger) *Handler {
	return &Handler{
		client:    client,
		repoStore: repoStore,
		oauthURL:  oauthURL,
		loginURL:  loginURL,
		publicURL: publicURL,
		log:       log,
	}
}

// gitFor returns a configured Bitbucket provider for the Slack team.
// Returns nil (no error) when the team has not connected Bitbucket yet.
func (h *Handler) gitFor(teamID string, refreshFn func(rec *store.TokenRecord) (*store.TokenRecord, error)) (provider.Provider, error) {
	ctx := context.Background()
	rec, err := h.repoStore.GetToken(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to look up credentials: %w", err)
	}
	if rec == nil {
		return nil, nil // caller should send connect prompt
	}

	// Refresh if expiring within 5 minutes.
	if time.Until(rec.ExpiresAt) < 5*time.Minute {
		rec, err = refreshFn(rec)
		if err != nil {
			return nil, fmt.Errorf("token refresh failed: %w", err)
		}
	}

	return provider.NewOAuth(rec.Workspace, rec.AccessToken), nil
}

// HandleSlashCommand routes slash commands to the appropriate handler.
func (h *Handler) HandleSlashCommand(cmd slack.SlashCommand, refreshFn func(rec *store.TokenRecord) (*store.TokenRecord, error)) {
	h.log.Info("slash command", "command", cmd.Command, "text", cmd.Text, "user", cmd.UserName, "team", cmd.TeamID)

	switch cmd.Command {
	case "/bb-prs":
		h.handlePRsCommand(cmd, refreshFn)
	case "/bb-repos":
		h.handleReposCommand(cmd, refreshFn)
	case "/repo":
		h.handleRepoCommand(cmd, refreshFn)
	case "/login":
		h.handleLoginCommand(cmd)
	default:
		h.respond(cmd.ChannelID, fmt.Sprintf("Unknown command: `%s`", cmd.Command))
	}
}

// HandleEvent routes Events API callbacks.
func (h *Handler) HandleEvent(event slackevents.EventsAPIEvent) error {
	if event.Type == slackevents.CallbackEvent {
		h.handleCallbackEvent(event)
	}
	return nil
}

func (h *Handler) handleCallbackEvent(event slackevents.EventsAPIEvent) {
	switch ev := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		h.log.Info("app mention", "user", ev.User, "text", ev.Text)
		h.respond(ev.Channel, fmt.Sprintf(
			"Hi <@%s>! Try `/bb-prs <repo>`, `/bb-repos`, or `/repo add <workspace/repo>`.\nNot connected yet? Use `/repo connect <workspace>`.",
			ev.User,
		))
	}
}

func (h *Handler) handlePRsCommand(cmd slack.SlashCommand, refreshFn func(*store.TokenRecord) (*store.TokenRecord, error)) {
	repo := strings.TrimSpace(cmd.Text)
	if repo == "" {
		h.respond(cmd.ChannelID, "Usage: `/bb-prs <repo-slug>`")
		return
	}

	git, err := h.gitFor(cmd.TeamID, refreshFn)
	if err != nil {
		h.respond(cmd.ChannelID, fmt.Sprintf(":x: %v", err))
		return
	}
	if git == nil {
		h.sendConnectPrompt(cmd.ChannelID, cmd.TeamID, "")
		return
	}

	prs, err := git.ListOpenPRs(repo)
	if err != nil {
		h.log.Error("list PRs failed", "repo", repo, "team", cmd.TeamID, "err", err)
		h.respond(cmd.ChannelID, fmt.Sprintf("Failed to fetch PRs for `%s`: %v", repo, err))
		return
	}

	if len(prs) == 0 {
		h.respond(cmd.ChannelID, fmt.Sprintf("No open pull requests in `%s`.", repo))
		return
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType,
			fmt.Sprintf("Open PRs in %s (%d)", repo, len(prs)), false, false)),
	}

	for _, pr := range prs {
		text := fmt.Sprintf("*<%s|#%d: %s>*\n%s → %s | by %s",
			pr.URL, pr.ID, pr.Title,
			pr.SourceBranch, pr.TargetBranch,
			pr.Author,
		)
		blocks = append(blocks,
			slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil),
			slack.NewDividerBlock(),
		)
	}

	_, _, err = h.client.PostMessage(cmd.ChannelID, slack.MsgOptionBlocks(blocks...))
	if err != nil {
		h.log.Error("post message failed", "err", err)
	}
}

func (h *Handler) handleReposCommand(cmd slack.SlashCommand, refreshFn func(*store.TokenRecord) (*store.TokenRecord, error)) {
	git, err := h.gitFor(cmd.TeamID, refreshFn)
	if err != nil {
		h.respond(cmd.ChannelID, fmt.Sprintf(":x: %v", err))
		return
	}
	if git == nil {
		h.sendConnectPrompt(cmd.ChannelID, cmd.TeamID, "")
		return
	}

	repos, err := git.ListRepos()
	if err != nil {
		h.log.Error("list repos failed", "team", cmd.TeamID, "err", err)
		h.respond(cmd.ChannelID, fmt.Sprintf("Failed to fetch repositories: %v", err))
		return
	}

	if len(repos) == 0 {
		h.respond(cmd.ChannelID, "No repositories found in workspace.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Repositories (%d)*\n", len(repos)))
	for _, r := range repos {
		sb.WriteString(fmt.Sprintf("• <%s|%s>", r.URL, r.FullName))
		if r.Description != "" {
			sb.WriteString(" — " + r.Description)
		}
		sb.WriteString("\n")
	}

	h.respond(cmd.ChannelID, sb.String())
}

// handleRepoCommand handles the /repo slash command with subcommands:
//
//	/repo connect <workspace>      — connect Bitbucket account via OAuth
//	/repo add <workspace/repo>     — subscribe this channel to PR notifications
//	/repo remove <workspace/repo>  — unsubscribe
//	/repo list                     — list subscriptions for this channel
func (h *Handler) handleRepoCommand(cmd slack.SlashCommand, refreshFn func(*store.TokenRecord) (*store.TokenRecord, error)) {
	const usage = "Usage: `/repo connect <workspace>`, `/repo add <workspace/repo>`, `/repo remove <workspace/repo>`, `/repo list`"

	parts := strings.Fields(cmd.Text)
	if len(parts) == 0 {
		h.respond(cmd.ChannelID, usage)
		return
	}

	ctx := context.Background()

	switch parts[0] {
	case "connect":
		if len(parts) < 2 {
			h.respond(cmd.ChannelID, "Usage: `/repo connect <workspace>`")
			return
		}
		workspace := parts[1]
		authURL := h.oauthURL(cmd.TeamID, cmd.ChannelID, workspace)
		h.respond(cmd.ChannelID, fmt.Sprintf(
			":key: Click the link below to connect Bitbucket workspace `%s` to this Slack team:\n<%s|Connect Bitbucket>",
			workspace, authURL,
		))

	case "add":
		if len(parts) < 2 {
			h.respond(cmd.ChannelID, "Usage: `/repo add <workspace/repo>`")
			return
		}
		repoSlug := normalizeRepoSlug(parts[1])

		rec, err := h.repoStore.GetToken(ctx, cmd.TeamID)
		if err != nil {
			h.respond(cmd.ChannelID, ":x: Failed to check connection status")
			return
		}
		if rec == nil {
			h.sendConnectPrompt(cmd.ChannelID, cmd.TeamID, "")
			return
		}

		if err := h.repoStore.Subscribe(ctx, cmd.ChannelID, cmd.TeamID, repoSlug); err != nil {
			h.log.Error("subscribe repo", "repo", repoSlug, "err", err)
			h.respond(cmd.ChannelID, fmt.Sprintf(":x: Failed to subscribe to `%s`", repoSlug))
			return
		}
		secret, err := h.repoStore.GetOrCreateWebhookSecret(ctx, repoSlug)
		if err != nil {
			h.log.Error("get webhook secret", "repo", repoSlug, "err", err)
			h.respond(cmd.ChannelID, ":x: Failed to generate webhook secret")
			return
		}
		webhookURL := h.publicURL + "/bitbucket/webhook"
		h.respond(cmd.ChannelID, fmt.Sprintf(
			":white_check_mark: This channel will now receive PR notifications for `%s`.\n\n"+
				"*Next step:* add this webhook in Bitbucket:\n"+
				"Repository → Settings → Webhooks → Add webhook\n"+
				"• URL: `%s`\n"+
				"• Secret: `%s`\n"+
				"• Trigger: *Pull Request → Created*",
			repoSlug, webhookURL, secret,
		))

	case "remove":
		if len(parts) < 2 {
			h.respond(cmd.ChannelID, "Usage: `/repo remove <workspace/repo>`")
			return
		}
		repoSlug := parts[1]
		if err := h.repoStore.Unsubscribe(ctx, cmd.ChannelID, repoSlug); err != nil {
			h.log.Error("unsubscribe repo", "repo", repoSlug, "err", err)
			h.respond(cmd.ChannelID, fmt.Sprintf(":x: Failed to unsubscribe from `%s`", repoSlug))
			return
		}
		h.respond(cmd.ChannelID, fmt.Sprintf(":white_check_mark: Unsubscribed from `%s`", repoSlug))

	case "list":
		repos, err := h.repoStore.ListForChannel(ctx, cmd.ChannelID)
		if err != nil {
			h.log.Error("list repo subscriptions", "channel", cmd.ChannelID, "err", err)
			h.respond(cmd.ChannelID, ":x: Failed to fetch subscriptions")
			return
		}
		if len(repos) == 0 {
			h.respond(cmd.ChannelID, "No repositories subscribed in this channel. Use `/repo add <workspace/repo>` to subscribe.")
			return
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("*Subscribed repositories (%d)*\n", len(repos)))
		for _, r := range repos {
			sb.WriteString(fmt.Sprintf("• `%s`\n", r))
		}
		h.respond(cmd.ChannelID, sb.String())

	default:
		h.respond(cmd.ChannelID, usage)
	}
}

// sendConnectPrompt posts a message asking the user to connect Bitbucket.
func (h *Handler) sendConnectPrompt(channelID, teamID, workspace string) {
	if workspace == "" {
		h.respond(channelID, ":warning: Bitbucket is not connected yet. Run `/repo connect <workspace>` to get started.")
		return
	}
	authURL := h.oauthURL(teamID, channelID, workspace)
	h.respond(channelID, fmt.Sprintf(
		":warning: Bitbucket is not connected yet. <%s|Click here to connect workspace `%s`>.",
		authURL, workspace,
	))
}

// normalizeRepoSlug extracts "workspace/repo" from either a full Bitbucket URL
// or a plain slug. Trailing slashes are stripped.
// e.g. "https://bitbucket.org/h1lary/test-repo/" → "h1lary/test-repo"
func normalizeRepoSlug(input string) string {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		u, err := url.Parse(input)
		if err == nil {
			// Path is like "/h1lary/test-repo" or "/h1lary/test-repo/"
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return strings.Trim(input, "/")
}

func (h *Handler) handleLoginCommand(cmd slack.SlashCommand) {
	authURL := h.loginURL(cmd.UserID, cmd.ChannelID)
	h.respond(cmd.ChannelID, fmt.Sprintf(
		":key: <@%s>, click the link below to link your Bitbucket account:\\n<%s|Connect your Bitbucket account>",
		cmd.UserID, authURL,
	))
}

func (h *Handler) respond(channelID, text string) {
	_, _, err := h.client.PostMessage(channelID, slack.MsgOptionText(text, false))
	if err != nil {
		h.log.Error("failed to post message", "channel", channelID, "err", err)
	}
}
