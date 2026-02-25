package slack

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"bitbucket-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
	slacklib "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// RegisterRoutes mounts all Slack webhook routes under the given router group.
func RegisterRoutes(router fiber.Router, h *Handler, signingSecret string, refreshFn func(*store.TokenRecord) (*store.TokenRecord, error)) {
	verified := router.Group("/slack", VerifySignature(signingSecret))

	verified.Post("/events", h.eventsRoute())
	verified.Post("/commands", h.commandsRoute(refreshFn))
	verified.Post("/interactions", h.interactionsRoute())
}

func (h *Handler) eventsRoute() fiber.Handler {
	return func(c *fiber.Ctx) error {
		body := c.Body()

		// Slack URL verification challenge
		var challenge struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &challenge); err == nil && challenge.Type == "url_verification" {
			return c.JSON(fiber.Map{"challenge": challenge.Challenge})
		}

		event, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
		if err != nil {
			h.log.Error("parse slack event", "err", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		go func() {
			if err := h.HandleEvent(event); err != nil {
				h.log.Error("handle event", "err", err)
			}
		}()

		return c.SendStatus(fiber.StatusOK)
	}
}

func (h *Handler) commandsRoute(refreshFn func(*store.TokenRecord) (*store.TokenRecord, error)) fiber.Handler {
	return func(c *fiber.Ctx) error {
		req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(c.Body()))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("internal error")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		cmd, err := slacklib.SlashCommandParse(req)
		if err != nil {
			h.log.Error("parse slash command", "err", err)
			return c.Status(fiber.StatusBadRequest).SendString("failed to parse command")
		}

		// Some commands are handled inline so their responses are ephemeral.
		if cmd.Command == "/login" {
			return c.JSON(h.loginResponse(cmd))
		}
		if cmd.Command == "/repo" {
			sub := strings.Fields(cmd.Text)
			if len(sub) > 0 && (sub[0] == "connect" || sub[0] == "add" || sub[0] == "list" || sub[0] == "delete") {
				return c.JSON(h.repoSubResponse(cmd))
			}
		}

		go h.HandleSlashCommand(cmd, refreshFn)

		// Empty JSON object: Slack silently acks without showing any message.
		return c.JSON(fiber.Map{})
	}
}

func (h *Handler) interactionsRoute() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Parse the form body the same way commandsRoute does — safe after VerifySignature.
		req, err := http.NewRequest(http.MethodPost, "/", bytes.NewReader(c.Body()))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("internal error")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := req.ParseForm(); err != nil {
			h.log.Error("parse interaction form", "err", err)
			return c.Status(fiber.StatusBadRequest).SendString("invalid form")
		}

		var payload slacklib.InteractionCallback
		if err := json.Unmarshal([]byte(req.FormValue("payload")), &payload); err != nil {
			h.log.Error("parse interaction payload", "err", err)
			return c.Status(fiber.StatusBadRequest).SendString("invalid payload")
		}

		// Ack immediately; HandleInteraction posts the updated message to response_url.
		go h.HandleInteraction(payload)
		return c.JSON(fiber.Map{})
	}
}
