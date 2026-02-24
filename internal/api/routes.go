package api

import "github.com/gofiber/fiber/v2"

// RegisterRoutes mounts the team-config management API.
// All routes require the Authorization: Bearer <api-key> header.
func RegisterRoutes(router fiber.Router, h *Handler, apiKey string) {
	g := router.Group("/api", requireAPIKey(apiKey))

	g.Get("/teams", h.listTeams)
	g.Post("/teams/:teamID/config", h.setTeamConfig)
	g.Get("/teams/:teamID/config", h.getTeamConfig)
	g.Delete("/teams/:teamID/config", h.deleteTeamConfig)
}

func requireAPIKey(key string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Get("Authorization") != "Bearer "+key {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}
