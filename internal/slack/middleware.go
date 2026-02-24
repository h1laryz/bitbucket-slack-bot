package slack

import (
	"bytes"
	"io"

	"github.com/gofiber/fiber/v2"
	slacklib "github.com/slack-go/slack"
)

// VerifySignature returns a Fiber middleware that validates Slack request signatures.
func VerifySignature(signingSecret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		body := c.Body()

		sv, err := slacklib.NewSecretsVerifier(c.GetReqHeaders(), signingSecret)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).SendString("invalid signature header")
		}

		if _, err = io.Copy(&sv, bytes.NewReader(body)); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("failed to read body")
		}

		if err = sv.Ensure(); err != nil {
			return c.Status(fiber.StatusUnauthorized).SendString("signature verification failed")
		}

		return c.Next()
	}
}
