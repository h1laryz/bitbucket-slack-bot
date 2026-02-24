package api

import (
	"git-slack-bot/internal/provider"
	"git-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	store *store.TeamStore
}

func NewHandler(store *store.TeamStore) *Handler {
	return &Handler{store: store}
}

// setTeamConfig registers or updates git credentials for a Slack team.
//
//	POST /api/teams/:teamID/config
//	Authorization: Bearer <api-key>
//
//	{
//	    "workspace": "my-workspace",
//	    "username":  "jdoe",
//	    "token":     "app-password",
//	    "base_url":  "https://api.bitbucket.org/2.0"  // optional
//	}
func (h *Handler) setTeamConfig(c *fiber.Ctx) error {
	teamID := c.Params("teamID")

	var body provider.TeamConfig
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}

	if body.Username == "" || body.Token == "" || body.Workspace == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "workspace, username and token are required",
		})
	}

	h.store.Set(teamID, body)
	return c.JSON(fiber.Map{"status": "ok", "team_id": teamID})
}

// getTeamConfig returns the current config for a Slack team (token is masked).
//
//	GET /api/teams/:teamID/config
func (h *Handler) getTeamConfig(c *fiber.Ctx) error {
	teamID := c.Params("teamID")

	cfg, err := h.store.Get(teamID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}

	cfg.Token = "***"
	return c.JSON(cfg)
}

// deleteTeamConfig removes a Slack team's git credentials.
//
//	DELETE /api/teams/:teamID/config
func (h *Handler) deleteTeamConfig(c *fiber.Ctx) error {
	h.store.Delete(c.Params("teamID"))
	return c.JSON(fiber.Map{"status": "deleted"})
}

// listTeams returns all configured Slack team IDs.
//
//	GET /api/teams
func (h *Handler) listTeams(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"teams": h.store.List()})
}
