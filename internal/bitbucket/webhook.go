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

	"bitbucket-slack-bot/internal/store"

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

// resolveReviewers resolves all PR reviewers to Slack mentions joined with ", ".
// Returns "—" when there are no reviewers.
func (h *WebhookHandler) resolveReviewers(ctx context.Context, pr bbPullRequest) string {
	if len(pr.Reviewers) == 0 {
		return "—"
	}
	names := make([]string, len(pr.Reviewers))
	for i, r := range pr.Reviewers {
		names[i] = h.resolveUser(ctx, r.DisplayName)
	}
	return strings.Join(names, ", ")
}

// prCard holds all data needed to build a PR Slack card.
type prCard struct {
	title        string
	prURL        string
	repoFullName string
	sourceBranch string
	destBranch   string
	authorLabel  string
	reviewers    string
	buildLabel   string
	statusLine   string
}

// getBuildLabel fetches the current build status from DB and formats it.
// Returns "—" if no build status is recorded.
func (h *WebhookHandler) getBuildLabel(ctx context.Context, repoSlug, commitHash string) string {
	if commitHash == "" {
		return "—"
	}
	bs, err := h.repoStore.GetBuildStatus(ctx, repoSlug, commitHash)
	if err != nil {
		h.log.Warn("get build status", "repo", repoSlug, "commit", commitHash, "err", err)
		return "—"
	}
	if bs == nil {
		return "—"
	}
	return formatBuildLabel(bs.State, bs.Name, bs.URL)
}

// formatBuildLabel formats a build state/name/url into a Slack-friendly label with emoji.
func formatBuildLabel(state, name, url string) string {
	var emoji string
	switch strings.ToUpper(state) {
	case "INPROGRESS":
		emoji = ":hourglass_flowing_sand:"
	case "SUCCESSFUL":
		emoji = ":white_check_mark:"
	case "FAILED":
		emoji = ":x:"
	case "STOPPED":
		emoji = ":octagonal_sign:"
	default:
		emoji = ":grey_question:"
	}
	if url != "" {
		return fmt.Sprintf("%s <%s|%s>", emoji, url, name)
	}
	return fmt.Sprintf("%s %s", emoji, name)
}

// buildCardFromPayload constructs a prCard from a PR webhook event payload,
// looking up the current build status from DB. Falls back to DB commit hash
// if the payload does not include one.
func (h *WebhookHandler) buildCardFromPayload(ctx context.Context, p bbEventPayload, statusLine string) prCard {
	author := h.resolveUser(ctx, p.PullRequest.Author.DisplayName)
	reviewers := h.resolveReviewers(ctx, p.PullRequest)

	commitHash := p.PullRequest.Source.Commit.Hash
	if commitHash == "" {
		if rec, _ := h.repoStore.GetPRCommit(ctx, p.Repository.FullName, p.PullRequest.ID); rec != nil {
			commitHash = rec.CommitHash
		}
	}

	return prCard{
		title:        p.PullRequest.Title,
		prURL:        p.PullRequest.Links.HTML.Href,
		repoFullName: p.Repository.FullName,
		sourceBranch: p.PullRequest.Source.Branch.Name,
		destBranch:   p.PullRequest.Destination.Branch.Name,
		authorLabel:  author,
		reviewers:    reviewers,
		buildLabel:   h.getBuildLabel(ctx, p.Repository.FullName, commitHash),
		statusLine:   statusLine,
	}
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
		"pullrequest:comment_created",
		"repo:commit_status_created",
		"repo:commit_status_updated":
		// handled below
	default:
		h.log.Info("ignoring event", "event", event)
		return c.SendStatus(fiber.StatusOK)
	}

	body := c.Body()

	// Commit status events have a different payload shape — route early.
	if event == "repo:commit_status_created" || event == "repo:commit_status_updated" {
		var p bbCommitStatusPayload
		if err := json.Unmarshal(body, &p); err != nil {
			h.log.Error("parse commit status payload", "err", err)
			return c.Status(fiber.StatusBadRequest).SendString("invalid payload")
		}
		go h.onCommitStatus(p)
		return c.SendStatus(fiber.StatusOK)
	}

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

// onPRCreated posts the initial PR notification and saves the message ts + PR commit info.
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

	card := h.buildCardFromPayload(ctx, p, "")
	blocks := buildPRBlocks(card)

	// Persist PR commit info so pipeline status events can find this PR later.
	reviewerNames := make([]string, len(p.PullRequest.Reviewers))
	for i, r := range p.PullRequest.Reviewers {
		reviewerNames[i] = r.DisplayName
	}
	if err := h.repoStore.SavePRCommit(ctx, store.PRCommitRecord{
		RepoSlug:      p.Repository.FullName,
		PRID:          p.PullRequest.ID,
		CommitHash:    p.PullRequest.Source.Commit.Hash,
		Title:         p.PullRequest.Title,
		URL:           p.PullRequest.Links.HTML.Href,
		AuthorName:    p.PullRequest.Author.DisplayName,
		ReviewerNames: reviewerNames,
		SourceBranch:  p.PullRequest.Source.Branch.Name,
		DestBranch:    p.PullRequest.Destination.Branch.Name,
	}); err != nil {
		h.log.Error("save PR commit", "repo", p.Repository.FullName, "pr", p.PullRequest.ID, "err", err)
	}

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
	card := h.buildCardFromPayload(ctx, p, fmt.Sprintf(":tada: Merged by %s", actor))
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, buildPRBlocks(card), card.statusLine)
}

// onPRDeclined updates the original message and posts a thread reply.
func (h *WebhookHandler) onPRDeclined(p bbEventPayload) {
	ctx := context.Background()
	actor := h.resolveUser(ctx, p.Actor.DisplayName)
	card := h.buildCardFromPayload(ctx, p, fmt.Sprintf(":x: Declined by %s", actor))
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, buildPRBlocks(card), card.statusLine)
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
	card := h.buildCardFromPayload(ctx, p, buildApprovalStatus(resolved))
	reply := fmt.Sprintf(":white_check_mark: %s approved this PR", actor)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, buildPRBlocks(card), reply)
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
	card := h.buildCardFromPayload(ctx, p, buildApprovalStatus(resolved))
	reply := fmt.Sprintf(":leftwards_arrow_with_hook: %s removed their approval", actor)
	h.updateAndReply(p.Repository.FullName, p.PullRequest.ID, buildPRBlocks(card), reply)
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

// onCommitStatus saves the build status, updates all Slack PR cards for that commit,
// and posts a thread reply describing the build result.
func (h *WebhookHandler) onCommitStatus(p bbCommitStatusPayload) {
	ctx := context.Background()
	repoSlug := p.Repository.FullName
	commitHash := p.CommitStatus.Commit.Hash

	if err := h.repoStore.SaveBuildStatus(ctx, repoSlug, commitHash,
		p.CommitStatus.State, p.CommitStatus.Name, p.CommitStatus.URL); err != nil {
		h.log.Error("save build status", "repo", repoSlug, "commit", commitHash, "err", err)
		return
	}

	prIDs, err := h.repoStore.GetPRsByCommit(ctx, repoSlug, commitHash)
	if err != nil {
		h.log.Error("get PRs by commit", "repo", repoSlug, "commit", commitHash, "err", err)
		return
	}

	buildLabel := formatBuildLabel(p.CommitStatus.State, p.CommitStatus.Name, p.CommitStatus.URL)
	replyText := buildStatusReply(p.CommitStatus.State, p.CommitStatus.Name, p.CommitStatus.URL)

	for _, prID := range prIDs {
		rec, err := h.repoStore.GetPRCommit(ctx, repoSlug, prID)
		if err != nil || rec == nil {
			continue
		}

		author := h.resolveUser(ctx, rec.AuthorName)
		reviewers := "—"
		if len(rec.ReviewerNames) > 0 {
			labels := make([]string, len(rec.ReviewerNames))
			for i, name := range rec.ReviewerNames {
				labels[i] = h.resolveUser(ctx, name)
			}
			reviewers = strings.Join(labels, ", ")
		}

		approvers, _ := h.repoStore.GetApprovals(ctx, repoSlug, prID)
		resolved := make([]string, len(approvers))
		for i, a := range approvers {
			resolved[i] = h.resolveUser(ctx, a)
		}

		card := prCard{
			title:        rec.Title,
			prURL:        rec.URL,
			repoFullName: repoSlug,
			sourceBranch: rec.SourceBranch,
			destBranch:   rec.DestBranch,
			authorLabel:  author,
			reviewers:    reviewers,
			buildLabel:   buildLabel,
			statusLine:   buildApprovalStatus(resolved),
		}

		msgs, err := h.repoStore.GetPRMessages(ctx, repoSlug, prID)
		if err != nil {
			h.log.Error("get PR messages", "repo", repoSlug, "pr", prID, "err", err)
			continue
		}
		blocks := buildPRBlocks(card)
		for _, msg := range msgs {
			if _, _, _, err := h.slack.UpdateMessage(msg.ChannelID, msg.MessageTS, slacklib.MsgOptionBlocks(blocks...)); err != nil {
				h.log.Error("update PR message on build status", "channel", msg.ChannelID, "err", err)
			}
			if _, _, err := h.slack.PostMessage(msg.ChannelID,
				slacklib.MsgOptionTS(msg.MessageTS),
				slacklib.MsgOptionText(replyText, false),
			); err != nil {
				h.log.Error("post build status thread reply", "channel", msg.ChannelID, "err", err)
			}
		}
		h.log.Info("PR card updated for build status", "repo", repoSlug, "pr", prID, "state", p.CommitStatus.State)
	}
}

// buildStatusReply formats a build state/name/url into a thread-reply string.
func buildStatusReply(state, name, url string) string {
	var prefix string
	switch strings.ToUpper(state) {
	case "INPROGRESS":
		prefix = ":hourglass_flowing_sand: Build started"
	case "SUCCESSFUL":
		prefix = ":white_check_mark: Build passed"
	case "FAILED":
		prefix = ":x: Build failed"
	case "STOPPED":
		prefix = ":octagonal_sign: Build stopped"
	default:
		prefix = ":grey_question: Build: " + state
	}
	if url != "" {
		return fmt.Sprintf("%s: <%s|%s>", prefix, url, name)
	}
	if name != "" {
		return prefix + ": " + name
	}
	return prefix
}

// buildApprovalStatus returns a status line listing all approvers, or "" if none.
func buildApprovalStatus(resolved []string) string {
	if len(resolved) == 0 {
		return ""
	}
	return ":white_check_mark: Approved by " + strings.Join(resolved, ", ")
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

// buildPRBlocks builds the Slack Block Kit message for a PR card.
//
// Layout:
//
//	Row 1: Pull request (bold link) | Repo (link)
//	Row 2: Build (emoji + link or "—") | Branch (source → dest)
//	Row 3: Reviewers (mentions or "—") | Author (mention)
//	[optional status context block]
func buildPRBlocks(card prCard) []slacklib.Block {
	repoURL := "https://bitbucket.org/" + card.repoFullName

	row1 := []*slacklib.TextBlockObject{
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Pull request*\n*<%s|%s>*", card.prURL, card.title), false, false),
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Repository*\n<%s|%s>", repoURL, card.repoFullName), false, false),
	}

	row2 := []*slacklib.TextBlockObject{
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Build*\n%s", card.buildLabel), false, false),
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Branch*\n`%s` → `%s`", card.sourceBranch, card.destBranch), false, false),
	}

	row3 := []*slacklib.TextBlockObject{
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Reviewers*\n%s", card.reviewers), false, false),
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*Author*\n%s", card.authorLabel), false, false),
	}

	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(nil, row1, nil),
		slacklib.NewDividerBlock(),
		slacklib.NewSectionBlock(nil, row2, nil),
		slacklib.NewDividerBlock(),
		slacklib.NewSectionBlock(nil, row3, nil),
		slacklib.NewDividerBlock(),
	}

	if card.statusLine != "" {
		blocks = append(blocks,
			slacklib.NewContextBlock("",
				slacklib.NewTextBlockObject(slacklib.MarkdownType, card.statusLine, false, false),
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
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	Author struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Reviewers []struct {
		DisplayName string `json:"display_name"`
	} `json:"reviewers"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// bbCommitStatusPayload covers repo:commit_status_created and repo:commit_status_updated events.
type bbCommitStatusPayload struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	CommitStatus struct {
		State  string `json:"state"`
		Name   string `json:"name"`
		URL    string `json:"url"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"commit_status"`
}
