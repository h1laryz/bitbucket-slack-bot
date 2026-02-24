package bitbucket

import "github.com/gofiber/fiber/v2"

func RegisterRoutes(router fiber.Router, wh *WebhookHandler, oh *OAuthHandler) {
	router.Post("/bitbucket/webhook", wh.Handle)
	router.Get("/bitbucket/oauth/callback", oh.HandleCallback)
}
