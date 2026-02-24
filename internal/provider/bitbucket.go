package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const bitbucketDefaultBaseURL = "https://api.bitbucket.org/2.0"

type bitbucketClient struct {
	baseURL    string
	workspace  string
	username   string
	token      string
	httpClient *http.Client
}

func newBitbucketClient(cfg TeamConfig) *bitbucketClient {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = bitbucketDefaultBaseURL
	}
	return &bitbucketClient{
		baseURL:    baseURL,
		workspace:  cfg.Workspace,
		username:   cfg.Username,
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *bitbucketClient) ListOpenPRs(repoSlug string) ([]PullRequest, error) {
	url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests?state=OPEN", c.baseURL, c.workspace, repoSlug)

	var raw struct {
		Values []bbPR `json:"values"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}

	prs := make([]PullRequest, len(raw.Values))
	for i, r := range raw.Values {
		prs[i] = r.toPR()
	}
	return prs, nil
}

func (c *bitbucketClient) GetPR(repoSlug string, prID int) (*PullRequest, error) {
	url := fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%d", c.baseURL, c.workspace, repoSlug, prID)

	var raw bbPR
	if err := c.get(url, &raw); err != nil {
		return nil, fmt.Errorf("get PR %d: %w", prID, err)
	}
	pr := raw.toPR()
	return &pr, nil
}

func (c *bitbucketClient) ListRepos() ([]Repository, error) {
	url := fmt.Sprintf("%s/repositories/%s", c.baseURL, c.workspace)

	var raw struct {
		Values []bbRepo `json:"values"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}

	repos := make([]Repository, len(raw.Values))
	for i, r := range raw.Values {
		repos[i] = r.toRepo()
	}
	return repos, nil
}

// --- Bitbucket API response shapes ---

type bbPR struct {
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

func (r bbPR) toPR() PullRequest {
	return PullRequest{
		ID:           r.ID,
		Title:        r.Title,
		Description:  r.Description,
		State:        r.State,
		Author:       r.Author.DisplayName,
		SourceBranch: r.Source.Branch.Name,
		TargetBranch: r.Destination.Branch.Name,
		URL:          r.Links.HTML.Href,
		CreatedAt:    r.CreatedOn,
	}
}

type bbRepo struct {
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

func (r bbRepo) toRepo() Repository {
	return Repository{
		Slug:        r.Slug,
		FullName:    r.FullName,
		Description: r.Description,
		IsPrivate:   r.IsPrivate,
		URL:         r.Links.HTML.Href,
	}
}

func (c *bitbucketClient) get(url string, out any) error {
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
		return fmt.Errorf("API error %d: %s", resp.StatusCode, body)
	}

	return json.Unmarshal(body, out)
}
