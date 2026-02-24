package slack

import (
	"fmt"
	"log/slog"
	"strings"

	"bitbucket-slack-bot/internal/bitbucket"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// Handler processes Slack events and slash commands.
type Handler struct {
	client *slack.Client
	bb     *bitbucket.Client
	log    *slog.Logger
}

func NewHandler(client *slack.Client, bb *bitbucket.Client, log *slog.Logger) *Handler {
	return &Handler{
		client: client,
		bb:     bb,
		log:    log,
	}
}

// HandleSlashCommand routes slash commands to the appropriate handler.
func (h *Handler) HandleSlashCommand(cmd slack.SlashCommand) {
	h.log.Info("slash command", "command", cmd.Command, "text", cmd.Text, "user", cmd.UserName)

	switch cmd.Command {
	case "/bb-prs":
		h.handlePRsCommand(cmd)
	case "/bb-repos":
		h.handleReposCommand(cmd)
	default:
		h.respond(cmd.ChannelID, fmt.Sprintf("Unknown command: `%s`", cmd.Command))
	}
}

// HandleEvent routes Events API callbacks.
func (h *Handler) HandleEvent(event slackevents.EventsAPIEvent) error {
	switch event.Type {
	case slackevents.CallbackEvent:
		h.handleCallbackEvent(event)
	}
	return nil
}

func (h *Handler) handleCallbackEvent(event slackevents.EventsAPIEvent) {
	switch ev := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		h.log.Info("app mention", "user", ev.User, "text", ev.Text)
		h.respond(ev.Channel, fmt.Sprintf("Hi <@%s>! Try `/bb-prs <repo>` or `/bb-repos`.", ev.User))
	}
}

func (h *Handler) handlePRsCommand(cmd slack.SlashCommand) {
	repo := strings.TrimSpace(cmd.Text)
	if repo == "" {
		h.respond(cmd.ChannelID, "Usage: `/bb-prs <repo-slug>`")
		return
	}

	prs, err := h.bb.ListOpenPRs(repo)
	if err != nil {
		h.log.Error("list PRs failed", "repo", repo, "err", err)
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
			pr.Links.HTML.Href, pr.ID, pr.Title,
			pr.Source.Branch.Name, pr.Destination.Branch.Name,
			pr.Author.DisplayName,
		)
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, text, false, false),
			nil, nil,
		))
		blocks = append(blocks, slack.NewDividerBlock())
	}

	_, _, err = h.client.PostMessage(cmd.ChannelID, slack.MsgOptionBlocks(blocks...))
	if err != nil {
		h.log.Error("post message failed", "err", err)
	}
}

func (h *Handler) handleReposCommand(cmd slack.SlashCommand) {
	repos, err := h.bb.ListRepos()
	if err != nil {
		h.log.Error("list repos failed", "err", err)
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
		sb.WriteString(fmt.Sprintf("• <%s|%s>", r.Links.HTML.Href, r.FullName))
		if r.Description != "" {
			sb.WriteString(" — " + r.Description)
		}
		sb.WriteString("\n")
	}

	h.respond(cmd.ChannelID, sb.String())
}

func (h *Handler) respond(channelID, text string) {
	_, _, err := h.client.PostMessage(channelID, slack.MsgOptionText(text, false))
	if err != nil {
		h.log.Error("failed to post message", "channel", channelID, "err", err)
	}
}
