package bitbucket

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"git-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
	slacklib "github.com/slack-go/slack"
)

// WebhookHandler processes incoming Bitbucket webhook events and forwards
// PR notifications to all Slack channels subscribed to that repository.
type WebhookHandler struct {
	slack     *slacklib.Client
	repoStore *store.RepoStore
	log       *slog.Logger
}

func NewWebhookHandler(slack *slacklib.Client, repoStore *store.RepoStore, log *slog.Logger) *WebhookHandler {
	return &WebhookHandler{slack: slack, repoStore: repoStore, log: log}
}

// resolveUser looks up the Slack user ID for a Bitbucket display name.
// Returns "<@USERID>" if a mapping exists, or "*DisplayName*" otherwise.
func (h *WebhookHandler) resolveUser(ctx context.Context, displayName string) string {
	id, err := h.repoStore.GetSlackUserByBitbucket(ctx, displayName)
	if err != nil {
		h.log.Warn("resolve user", "bitbucket", displayName, "err", err)
	}
	if id != "" {
		return "<@" + id + ">"
	}
	return "*" + displayName + "*"
}

// Handle routes Bitbucket webhook events.
func (h *WebhookHandler) Handle(c *fiber.Ctx) error {
	event := c.Get("X-Event-Key")
	h.log.Info("bitbucket webhook received", "event", event)

	switch event {
	case "pullrequest:created",
		"pullrequest:fulfilled",
		"pullrequest:rejected",
		"pullrequest:approved",
		"pullrequest:unapproved",
		"pullrequest:comment_created":
		// handled below
	default:
		h.log.Info("ignoring event", "event", event)
		return c.SendStatus(fiber.StatusOK)
	}

	body := c.Body()

	var payload bbEventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.log.Error("parse bitbucket webhook", "err", err)
		return c.Status(fiber.StatusBadRequest).SendString("invalid payload")
	}

	// Verify HMAC signature if a secret is configured for this repo.
	secret, err := h.repoStore.GetWebhookSecret(c.Context(), payload.Repository.FullName)
	if err != nil {
		h.log.Error("get webhook secret", "repo", payload.Repository.FullName, "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("internal error")
	}
	if secret != "" {
		if !verifySignature(secret, body, c.Get("X-Hub-Signature")) {
			h.log.Warn("webhook signature mismatch", "repo", payload.Repository.FullName)
			return c.Status(fiber.StatusUnauthorized).SendString("invalid signature")
		}
	}

	switch event {
	case "pullrequest:created":
		h.log.Info("PR created", "repo", payload.Repository.FullName, "pr_id", payload.PullRequest.ID, "title", payload.PullRequest.Title)
		go h.onPRCreated(payload)
	case "pullrequest:fulfilled":
		h.log.Info("PR merged", "repo", payload.Repository.FullName, "pr_id", payload.PullRequest.ID)
		go h.onPRMerged(payload)
	case "pullrequest:rejected":
		h.log.Info("PR declined", "repo", payload.Repository.FullName, "pr_id", payload.PullRequest.ID)
		go h.onPRDeclined(payload)
	case "pullrequest:approved":
		go h.onPRApproved(payload)
	case "pullrequest:unapproved":
		go h.onPRUnapproved(payload)
	case "pullrequest:comment_created":
		go h.onPRComment(payload)
	}

	return c.SendStatus(fiber.StatusOK)
}

// onPRCreated posts the initial PR notification and saves the message ts.
func (h *WebhookHandler) onPRCreated(p bbEventPayload) {
	ctx := context.Background()
	channels, err := h.repoStore.ChannelsForRepo(ctx, p.Repository.FullName)
	if err != nil {
		h.log.Error("look up channels for repo", "repo", p.Repository.FullName, "err", err)
		return
	}
	if len(channels) == 0 {
		h.log.Info("no subscribers for repo", "repo", p.Repository.FullName)
		return
	}

	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	blocks := buildPRBlocks(p.PullRequest, p.Repository.FullName, author, "")

	for _, channelID := range channels {
		_, ts, err := h.slack.PostMessage(channelID, slacklib.MsgOptionBlocks(blocks...))
		if err != nil {
			h.log.Error("post PR notification", "channel", channelID, "err", err)
			continue
		}
		if err := h.repoStore.SavePRMessage(ctx, p.Repository.FullName, p.PullRequest.ID, channelID, ts); err != nil {
			h.log.Error("save PR message ts", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
		}
	}

	h.log.Info("PR notification sent", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "channels", len(channels))
}

// onPRMerged updates the original message and posts a thread reply.
func (h *WebhookHandler) onPRMerged(p bbEventPayload) {
	ctx := context.Background()
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	status := fmt.Sprintf(":tada: Merged by %s", actor)
	blocks := buildPRBlocks(p.PullRequest, p.Repository.FullName, author, status)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, blocks, status)
}

// onPRDeclined updates the original message and posts a thread reply.
func (h *WebhookHandler) onPRDeclined(p bbEventPayload) {
	ctx := context.Background()
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	status := fmt.Sprintf(":x: Declined by %s", actor)
	blocks := buildPRBlocks(p.PullRequest, p.Repository.FullName, author, status)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, blocks, status)
}

// onPRApproved records the approval, rebuilds the approvers context block, and posts a thread reply.
func (h *WebhookHandler) onPRApproved(p bbEventPayload) {
	ctx := context.Background()
	if err := h.repoStore.AddApproval(ctx, p.Repository.FullName, p.PullRequest.ID, p.Actor.DisplayName); err != nil {
		h.log.Error("add approval", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
	}
	approvers, err := h.repoStore.GetApprovals(ctx, p.Repository.FullName, p.PullRequest.ID)
	if err != nil {
		h.log.Error("get approvals", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
	}

	resolved := make([]string, len(approvers))
	for i, a := range approvers {
		resolved[i] = h.resolveUser(ctx, a)
	}
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	statusLine := buildApprovalStatus(resolved)
	blocks := buildPRBlocks(p.PullRequest, p.Repository.FullName, author, statusLine)
	reply := fmt.Sprintf(":white_check_mark: %s approved this PR", actor)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, blocks, reply)
}

// onPRUnapproved removes the approval, rebuilds the approvers context block, and posts a thread reply.
func (h *WebhookHandler) onPRUnapproved(p bbEventPayload) {
	ctx := context.Background()
	if err := h.repoStore.RemoveApproval(ctx, p.Repository.FullName, p.PullRequest.ID, p.Actor.DisplayName); err != nil {
		h.log.Error("remove approval", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
	}
	approvers, err := h.repoStore.GetApprovals(ctx, p.Repository.FullName, p.PullRequest.ID)
	if err != nil {
		h.log.Error("get approvals", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
	}

	resolved := make([]string, len(approvers))
	for i, a := range approvers {
		resolved[i] = h.resolveUser(ctx, a)
	}
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	statusLine := buildApprovalStatus(resolved)
	blocks := buildPRBlocks(p.PullRequest, p.Repository.FullName, author, statusLine)
	reply := fmt.Sprintf(":leftwards_arrow_with_hook: %s removed their approval", actor)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, blocks, reply)
}

// buildApprovalStatus returns a context-block status line listing all approvers (pre-resolved),
// or "" if there are none.
func buildApprovalStatus(resolved []string) string {
	if len(resolved) == 0 {
		return ""
	}
	return ":white_check_mark: Approved by " + strings.Join(resolved, ", ")
}

// onPRComment posts the comment text as a thread reply.
func (h *WebhookHandler) onPRComment(p bbEventPayload) {
	ctx := context.Background()
	text := p.Comment.Content.Raw
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	reply := fmt.Sprintf(":speech_balloon: %s commented:\n>%s", actor, text)
	h.threadReply(p.Repository.FullName, p.PullRequest.ID, reply)
}

// updateAndReply updates the original Slack message and posts a thread reply.
// Falls back to a new standalone message if no ts is stored.
func (h *WebhookHandler) updateAndReply(repoSlug string, prID int, blocks []slacklib.Block, replyText string) {
	ctx := context.Background()
	msgs, err := h.repoStore.GetPRMessages(ctx, repoSlug, prID)
	if err != nil {
		h.log.Error("get PR messages", "repo", repoSlug, "pr", prID, "err", err)
		return
	}

	if len(msgs) == 0 {
		// No ts stored — post a standalone message to all subscribed channels.
		channels, _ := h.repoStore.ChannelsForRepo(ctx, repoSlug)
		for _, ch := range channels {
			h.slack.PostMessage(ch, slacklib.MsgOptionBlocks(blocks...))
		}
		return
	}

	for _, msg := range msgs {
		if _, _, _, err := h.slack.UpdateMessage(msg.ChannelID, msg.MessageTS, slacklib.MsgOptionBlocks(blocks...)); err != nil {
			h.log.Error("update PR message", "channel", msg.ChannelID, "err", err)
		}
		if _, _, err := h.slack.PostMessage(msg.ChannelID,
			slacklib.MsgOptionTS(msg.MessageTS),
			slacklib.MsgOptionText(replyText, false),
		); err != nil {
			h.log.Error("post thread reply", "channel", msg.ChannelID, "err", err)
		}
	}
}

// threadReply posts text as a thread reply to the original PR message.
func (h *WebhookHandler) threadReply(repoSlug string, prID int, text string) {
	ctx := context.Background()
	msgs, err := h.repoStore.GetPRMessages(ctx, repoSlug, prID)
	if err != nil {
		h.log.Error("get PR messages", "repo", repoSlug, "pr", prID, "err", err)
		return
	}
	for _, msg := range msgs {
		if _, _, err := h.slack.PostMessage(msg.ChannelID,
			slacklib.MsgOptionTS(msg.MessageTS),
			slacklib.MsgOptionText(text, false),
		); err != nil {
			h.log.Error("post thread reply", "channel", msg.ChannelID, "err", err)
		}
	}
}

// buildPRBlocks builds the Slack Block Kit message for a PR.
// statusLine is shown as a context block at the bottom (e.g. ":tada: Merged by *X*").
func buildPRBlocks(pr bbPullRequest, repoFullName, authorLabel, statusLine string) []slacklib.Block {
	repoURL := "https://bitbucket.org/" + repoFullName

	topRow := []*slacklib.TextBlockObject{
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Pull request*\n*<%s|%s>*", pr.Links.HTML.Href, pr.Title), false, false),
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Repo*\n<%s|%s>", repoURL, repoFullName), false, false),
	}

	bottomRow := []*slacklib.TextBlockObject{
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Author*\n%s", authorLabel), false, false),
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Branch*\n`%s` → `%s`", pr.Source.Branch.Name, pr.Destination.Branch.Name), false, false),
	}

	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(nil, topRow, nil),
		slacklib.NewDividerBlock(),
		slacklib.NewSectionBlock(nil, bottomRow, nil),
		slacklib.NewDividerBlock(),
	}

	if statusLine != "" {
		blocks = append(blocks,
			slacklib.NewContextBlock("",
				slacklib.NewTextBlockObject(slacklib.MarkdownType, statusLine, false, false),
			),
		)
	}

	return blocks
}

// verifySignature checks the X-Hub-Signature header against HMAC-SHA256(secret, body).
func verifySignature(secret string, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// bbEventPayload covers all Bitbucket PR event types.
type bbEventPayload struct {
	Actor struct {
		DisplayName string `json:"display_name"`
	} `json:"actor"`
	PullRequest bbPullRequest `json:"pullrequest"`
	Repository  struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Comment struct {
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	} `json:"comment"`
}

type bbPullRequest struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	Author struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}
