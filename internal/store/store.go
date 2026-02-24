package store

import (
	"fmt"
	"sync"

	"git-slack-bot/internal/provider"
)

// TeamStore keeps per-Slack-team git credentials in memory.
// One bot instance can serve many Slack workspaces, each with its own
// git provider credentials set via the management API.
type TeamStore struct {
	mu    sync.RWMutex
	teams map[string]provider.TeamConfig
}

func New() *TeamStore {
	return &TeamStore{teams: make(map[string]provider.TeamConfig)}
}

// Set stores or replaces the config for a Slack team.
func (s *TeamStore) Set(teamID string, cfg provider.TeamConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teams[teamID] = cfg
}

// Get returns the config for a Slack team, or an error if not yet configured.
func (s *TeamStore) Get(teamID string) (provider.TeamConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.teams[teamID]
	if !ok {
		return provider.TeamConfig{}, fmt.Errorf(
			"team %q has no git config — call POST /api/teams/%s/config first", teamID, teamID,
		)
	}
	return cfg, nil
}

// Delete removes a team's config.
func (s *TeamStore) Delete(teamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.teams, teamID)
}

// List returns all configured team IDs.
func (s *TeamStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.teams))
	for id := range s.teams {
		ids = append(ids, id)
	}
	return ids
}
