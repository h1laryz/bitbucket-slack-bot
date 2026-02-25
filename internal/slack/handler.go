package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bitbucket-slack-bot/internal/provider"
	"bitbucket-slack-bot/internal/store"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// Handler processes Slack events and slash commands.
type Handler struct {
	client    *slack.Client
	repoStore *store.RepoStore
	oauthURL  func(teamID, channelID, userID, workspace string) string
	loginURL  func(slackUserID, channelID string) string
	publicURL string
	log       *slog.Logger
}

func NewHandler(client *slack.Client, repoStore *store.RepoStore, oauthURL func(teamID, channelID, userID, workspace string) string, loginURL func(slackUserID, channelID string) string, publicURL string, log *slog.Logger) *Handler {
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
	case "/repo":
		h.handleRepoCommand(cmd)
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
			"Hi <@%s>! Use `/repo connect <workspace>` to connect Bitbucket, `/repo add <workspace/repo>` to subscribe a channel, or `/repo list` to see subscriptions.",
			ev.User,
		))
	}
}

// handleRepoCommand handles the /repo slash command with subcommands:
//
//	/repo connect <workspace>   — connect Bitbucket account via OAuth
//	/repo add <workspace/repo>  — subscribe this channel to PR notifications
//	/repo list                  — list subscriptions (ephemeral)
//	/repo delete                — remove subscriptions via buttons (ephemeral)
func (h *Handler) handleRepoCommand(cmd slack.SlashCommand) {
	const usage = "Usage: `/repo connect <workspace>`, `/repo add <workspace/repo>`, `/repo list`, `/repo delete`"

	parts := strings.Fields(cmd.Text)
	if len(parts) == 0 {
		h.respond(cmd.ChannelID, usage)
		return
	}

	switch parts[0] {
	default:
		h.respond(cmd.ChannelID, usage)
	}
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

// slashResponse is the JSON body returned directly to a slash command request.
type slashResponse struct {
	ResponseType string        `json:"response_type"`
	Text         string        `json:"text,omitempty"`
	Blocks       []slack.Block `json:"blocks,omitempty"`
}

// interactionReply is the JSON body returned to a Slack block-actions interaction.
type interactionReply struct {
	ReplaceOriginal bool          `json:"replace_original"`
	Text            string        `json:"text,omitempty"`
	Blocks          []slack.Block `json:"blocks,omitempty"`
}

// repoSubResponse handles /repo connect and /repo add inline, returning an ephemeral response.
func (h *Handler) repoSubResponse(cmd slack.SlashCommand) slashResponse {
	parts := strings.Fields(cmd.Text)
	switch parts[0] {
	case "connect":
		if len(parts) < 2 {
			return slashResponse{ResponseType: "ephemeral", Text: "Usage: `/repo connect <workspace>`"}
		}
		workspace := parts[1]
		authURL := h.oauthURL(cmd.TeamID, cmd.ChannelID, cmd.UserID, workspace)
		return slashResponse{
			ResponseType: "ephemeral",
			Text: fmt.Sprintf(
				":key: Click the link below to connect Bitbucket workspace `%s` to this Slack team:\n<%s|Connect Bitbucket>",
				workspace, authURL,
			),
		}

	case "add":
		if len(parts) < 2 {
			return slashResponse{ResponseType: "ephemeral", Text: "Usage: `/repo add <workspace/repo>`"}
		}
		repoSlug := normalizeRepoSlug(parts[1])
		ctx := context.Background()

		rec, err := h.repoStore.GetToken(ctx, cmd.TeamID)
		if err != nil {
			return slashResponse{ResponseType: "ephemeral", Text: ":x: Failed to check connection status"}
		}
		if rec == nil {
			return slashResponse{ResponseType: "ephemeral", Text: ":warning: Bitbucket is not connected yet. Run `/repo connect <workspace>` to get started."}
		}

		if err := h.repoStore.Subscribe(ctx, cmd.ChannelID, cmd.TeamID, repoSlug); err != nil {
			h.log.Error("subscribe repo", "repo", repoSlug, "err", err)
			return slashResponse{ResponseType: "ephemeral", Text: fmt.Sprintf(":x: Failed to subscribe to `%s`", repoSlug)}
		}

		secret, err := h.repoStore.GetOrCreateWebhookSecret(ctx, repoSlug)
		if err != nil {
			h.log.Error("get webhook secret", "repo", repoSlug, "err", err)
			return slashResponse{ResponseType: "ephemeral", Text: ":x: Failed to generate webhook secret"}
		}

		webhookURL := h.publicURL + "/bitbucket/webhook"
		return slashResponse{
			ResponseType: "ephemeral",
			Text: fmt.Sprintf(
				":white_check_mark: This channel will now receive PR notifications for `%s`.\n\n"+
					"*Next step:* add this webhook in Bitbucket:\n"+
					"Repository → Settings → Webhooks → Add webhook\n"+
					"• URL: `%s`\n"+
					"• Secret: `%s`\n"+
					"• Triggers: *ALL*",
				repoSlug, webhookURL, secret,
			),
		}

	case "list":
		ctx := context.Background()
		repos, err := h.repoStore.ListForChannel(ctx, cmd.ChannelID)
		if err != nil {
			return slashResponse{ResponseType: "ephemeral", Text: ":x: Failed to fetch subscriptions"}
		}
		if len(repos) == 0 {
			return slashResponse{ResponseType: "ephemeral", Text: "No repositories subscribed in this channel."}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("*Subscribed repositories (%d)*\n", len(repos)))
		for _, r := range repos {
			sb.WriteString(fmt.Sprintf("• `%s`\n", normalizeRepoSlug(r)))
		}
		return slashResponse{ResponseType: "ephemeral", Text: sb.String()}

	case "delete":
		ctx := context.Background()
		repos, err := h.repoStore.ListForChannel(ctx, cmd.ChannelID)
		if err != nil {
			return slashResponse{ResponseType: "ephemeral", Text: ":x: Failed to fetch subscriptions"}
		}
		return slashResponse{ResponseType: "ephemeral", Blocks: buildRepoDeleteBlocks(repos)}

	}

	return slashResponse{ResponseType: "ephemeral", Text: "Usage: `/repo connect <workspace>`, `/repo add <workspace/repo>`, `/repo list`, `/repo delete`"}
}

// buildRepoDeleteBlocks builds a Block Kit list of repos with a Delete button on each row.
func buildRepoDeleteBlocks(repos []string) []slack.Block {
	if len(repos) == 0 {
		return []slack.Block{
			slack.NewSectionBlock(
				slack.NewTextBlockObject(slack.MarkdownType, "No repositories subscribed in this channel.", false, false),
				nil, nil,
			),
		}
	}
	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType,
				fmt.Sprintf("*Subscribed repositories (%d)*\nClick *Delete* to unsubscribe:", len(repos)),
				false, false),
			nil, nil,
		),
		slack.NewDividerBlock(),
	}
	for _, repo := range repos {
		display := normalizeRepoSlug(repo)
		btn := slack.NewButtonBlockElement("repo_delete", repo,
			slack.NewTextBlockObject(slack.PlainTextType, "Delete", false, false))
		btn.Style = slack.StyleDanger
		blocks = append(blocks,
			slack.NewSectionBlock(
				slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("`%s`", display), false, false),
				nil,
				slack.NewAccessory(btn),
			),
		)
	}
	return blocks
}

// HandleInteraction processes Slack block_actions payloads (e.g. Delete repo buttons).
// It posts the updated message to payload.ResponseURL so ephemeral messages are updated correctly.
func (h *Handler) HandleInteraction(payload slack.InteractionCallback) {
	if payload.Type != slack.InteractionTypeBlockActions {
		return
	}

	for _, action := range payload.ActionCallback.BlockActions {
		if action.ActionID == "repo_delete" {
			channelID := payload.Channel.ID
			repoSlug := action.Value

			if err := h.repoStore.Unsubscribe(context.Background(), channelID, repoSlug); err != nil {
				h.log.Error("unsubscribe repo via button", "repo", repoSlug, "err", err)
			}

			repos, _ := h.repoStore.ListForChannel(context.Background(), channelID)
			confirm := slack.NewSectionBlock(
				slack.NewTextBlockObject(slack.MarkdownType,
					fmt.Sprintf(":white_check_mark: Unsubscribed from `%s`", normalizeRepoSlug(repoSlug)),
					false, false),
				nil, nil,
			)
			h.postToResponseURL(payload.ResponseURL, interactionReply{
				ReplaceOriginal: true,
				Blocks:          append([]slack.Block{confirm, slack.NewDividerBlock()}, buildRepoDeleteBlocks(repos)...),
			})
			return
		}
	}
}

// postToResponseURL POSTs a JSON reply to a Slack response_url.
func (h *Handler) postToResponseURL(responseURL string, reply interactionReply) {
	body, err := json.Marshal(reply)
	if err != nil {
		h.log.Error("marshal interaction reply", "err", err)
		return
	}
	resp, err := http.Post(responseURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		h.log.Error("post to response_url", "err", err)
		return
	}
	resp.Body.Close()
}

// loginResponse builds an ephemeral inline response for the /login command.
func (h *Handler) loginResponse(cmd slack.SlashCommand) slashResponse {
	if cmd.ChannelName != "directmessage" {
		return slashResponse{
			ResponseType: "ephemeral",
			Text:         ":lock: `/login` can only be used in a direct message with the bot.",
		}
	}
	authURL := h.loginURL(cmd.UserID, cmd.ChannelID)
	return slashResponse{
		ResponseType: "ephemeral",
		Text: fmt.Sprintf(
			":key: <@%s>, click the link below to link your Bitbucket account:\n<%s|Connect your Bitbucket account>",
			cmd.UserID, authURL,
		),
	}
}

func (h *Handler) respond(channelID, text string) {
	_, _, err := h.client.PostMessage(channelID, slack.MsgOptionText(text, false))
	if err != nil {
		h.log.Error("failed to post message", "channel", channelID, "err", err)
	}
}
