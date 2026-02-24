package bitbucket

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	workspace  string
	username   string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, workspace, username, token string) *Client {
	return &Client{
		baseURL:   baseURL,
		workspace: workspace,
		username:  username,
		token:     token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// PullRequest represents a Bitbucket pull request.
type PullRequest struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Author      struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
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
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	CreatedOn time.Time `json:"created_on"`
}

type prListResponse struct {
	Values []PullRequest `json:"values"`
	Next   string        `json:"next"`
}

// Repository represents a Bitbucket repository.
type Repository struct {
	Slug        string `json:"slug"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

type repoListResponse struct {
	Values []Repository `json:"values"`
}

// ListOpenPRs returns open pull requests for a repository.
func (c *Client) ListOpenPRs(repoSlug string) ([]PullRequest, error) {
	url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests?state=OPEN", c.baseURL, c.workspace, repoSlug)

	var result prListResponse
	if err := c.get(url, &result); err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}

	return result.Values, nil
}

// GetPR returns a single pull request by ID.
func (c *Client) GetPR(repoSlug string, prID int) (*PullRequest, error) {
	url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d", c.baseURL, c.workspace, repoSlug, prID)

	var pr PullRequest
	if err := c.get(url, &pr); err != nil {
		return nil, fmt.Errorf("get PR %d: %w", prID, err)
	}

	return &pr, nil
}

// ListRepos returns repositories in the workspace.
func (c *Client) ListRepos() ([]Repository, error) {
	url := fmt.Sprintf("%s/repositories/%s", c.baseURL, c.workspace)

	var result repoListResponse
	if err := c.get(url, &result); err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}

	return result.Values, nil
}

func (c *Client) get(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.username, c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("bitbucket API error %d: %s", resp.StatusCode, string(body))
	}

	return json.Unmarshal(body, out)
}
