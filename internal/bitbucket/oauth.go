package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git-slack-bot/internal/store"

	"github.com/gofiber/fiber/v2"
	slacklib "github.com/slack-go/slack"
)

const bitbucketTokenURL = "https://bitbucket.org/site/oauth2/access_token"

// OAuthHandler handles the Bitbucket OAuth2 callback and token refresh.
type OAuthHandler struct {
	clientID     string
	clientSecret string
	publicURL    string
	repoStore    *store.RepoStore
	slack        *slacklib.Client
	log          *slog.Logger
}

func NewOAuthHandler(clientID, clientSecret, publicURL string, repoStore *store.RepoStore, slack *slacklib.Client, log *slog.Logger) *OAuthHandler {
	return &OAuthHandler{
		clientID:     clientID,
		clientSecret: clientSecret,
		publicURL:    publicURL,
		repoStore:    repoStore,
		slack:        slack,
		log:          log,
	}
}

// AuthURL returns the Bitbucket OAuth2 authorization URL for workspace connect.
// state encodes "connect:teamID:channelID:workspace".
func (h *OAuthHandler) AuthURL(teamID, channelID, workspace string) string {
	state := "connect:" + teamID + ":" + channelID + ":" + workspace
	return fmt.Sprintf(
		"https://bitbucket.org/site/oauth2/authorize?client_id=%s&response_type=code&state=%s",
		h.clientID, url.QueryEscape(state),
	)
}

// AuthLoginURL returns the Bitbucket OAuth2 authorization URL for user identity linking.
// state encodes "login:slackUserID:channelID".
func (h *OAuthHandler) AuthLoginURL(slackUserID, channelID string) string {
	state := "login:" + slackUserID + ":" + channelID
	return fmt.Sprintf(
		"https://bitbucket.org/site/oauth2/authorize?client_id=%s&response_type=code&state=%s",
		h.clientID, url.QueryEscape(state),
	)
}

// HandleCallback processes the OAuth2 redirect from Bitbucket.
// Dispatches to handleConnect or handleLogin based on the state prefix.
func (h *OAuthHandler) HandleCallback(c *fiber.Ctx) error {
	code := c.Query("code")
	state := c.Query("state")

	if code == "" || state == "" {
		return c.Status(fiber.StatusBadRequest).SendString("missing code or state")
	}

	if strings.HasPrefix(state, "login:") {
		return h.handleLogin(c, code, strings.TrimPrefix(state, "login:"))
	}
	if strings.HasPrefix(state, "connect:") {
		return h.handleConnect(c, code, strings.TrimPrefix(state, "connect:"))
	}
	return c.Status(fiber.StatusBadRequest).SendString("invalid state")
}

func (h *OAuthHandler) handleConnect(c *fiber.Ctx, code, stateBody string) error {
	parts := strings.SplitN(stateBody, ":", 3)
	if len(parts) != 3 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid state")
	}
	teamID, channelID, workspace := parts[0], parts[1], parts[2]

	token, err := h.exchangeCode(code)
	if err != nil {
		h.log.Error("oauth code exchange failed", "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to exchange code")
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	if err := h.repoStore.SaveToken(c.Context(), teamID, workspace, token.AccessToken, token.RefreshToken, expiresAt); err != nil {
		h.log.Error("save token failed", "team", teamID, "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to save token")
	}

	h.log.Info("bitbucket workspace connected", "team", teamID, "workspace", workspace)
	_, _, _ = h.slack.PostMessage(channelID, slacklib.MsgOptionText(
		fmt.Sprintf(":white_check_mark: Bitbucket workspace `%s` connected! You can now use `/bb-prs`, `/bb-repos`, and `/repo add`.", workspace),
		false,
	))
	return c.SendString("Bitbucket connected! You can close this tab and return to Slack.")
}

func (h *OAuthHandler) handleLogin(c *fiber.Ctx, code, stateBody string) error {
	parts := strings.SplitN(stateBody, ":", 2)
	if len(parts) != 2 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid state")
	}
	slackUserID, channelID := parts[0], parts[1]

	token, err := h.exchangeCode(code)
	if err != nil {
		h.log.Error("login code exchange failed", "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to exchange code")
	}

	bbUser, err := h.fetchBitbucketUser(token.AccessToken)
	if err != nil {
		h.log.Error("fetch bitbucket user failed", "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to fetch Bitbucket user")
	}

	if err := h.repoStore.SaveUserMapping(c.Context(), slackUserID, bbUser.DisplayName); err != nil {
		h.log.Error("save user mapping failed", "slack_user", slackUserID, "err", err)
		return c.Status(fiber.StatusInternalServerError).SendString("failed to save user mapping")
	}

	h.log.Info("user linked", "slack_user", slackUserID, "bitbucket_user", bbUser.DisplayName)
	_, _, _ = h.slack.PostMessage(channelID, slacklib.MsgOptionText(
		fmt.Sprintf(":white_check_mark: <@%s> linked to Bitbucket account *%s*. PR notifications will now mention you directly.", slackUserID, bbUser.DisplayName),
		false,
	))
	return c.SendString("Bitbucket account linked! You can close this tab and return to Slack.")
}

func (h *OAuthHandler) fetchBitbucketUser(accessToken string) (*bbUser, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.bitbucket.org/2.0/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("bitbucket user API %d: %s", resp.StatusCode, body)
	}

	var u bbUser
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

type bbUser struct {
	DisplayName string `json:"display_name"`
	AccountID   string `json:"account_id"`
}

// RefreshTokenBg exchanges a refresh token for a new access token and saves it.
// Uses a plain context.Context (for use outside of HTTP request handlers).
func (h *OAuthHandler) RefreshTokenBg(ctx context.Context, rec *store.TokenRecord) (*store.TokenRecord, error) {
	token, err := h.doTokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rec.RefreshToken},
	})
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	if err := h.repoStore.SaveToken(ctx, rec.TeamID, rec.Workspace, token.AccessToken, token.RefreshToken, expiresAt); err != nil {
		return nil, err
	}

	return &store.TokenRecord{
		TeamID:       rec.TeamID,
		Workspace:    rec.Workspace,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

type bbTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (h *OAuthHandler) exchangeCode(code string) (*bbTokenResponse, error) {
	return h.doTokenRequest(url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
	})
}

func (h *OAuthHandler) doTokenRequest(params url.Values) (*bbTokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, bitbucketTokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(h.clientID, h.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("token request failed %d: %s", resp.StatusCode, body)
	}

	var t bbTokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
