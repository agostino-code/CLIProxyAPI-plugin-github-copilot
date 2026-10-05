package provider

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"cliproxyapi-github-copilot/internal/transport"
)

// CatalogRevisionKey is retained to preserve metadata from older credentials.
// Catalog refreshes must never write this marker back to a host auth file.
const CatalogRevisionKey = "github_copilot_catalog_revision"

const hostCatalogSyncUnavailable = "automatic host model registry refresh unavailable; registry may remain stale until the host requests models again; local catalog enforcement remains active"

type AccountStatus struct {
	ID             string    `json:"id"`
	Login          string    `json:"login"`
	Models         []string  `json:"models"`
	LastSuccess    time.Time `json:"last_successful_discovery"`
	ExpiresAt      time.Time `json:"catalog_expires_at"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	Error          string    `json:"error,omitempty"`
	SyncError      string    `json:"sync_error,omitempty"`
}

func (s *Service) Status() any {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	accounts := make([]AccountStatus, 0, len(s.accounts))
	for _, a := range s.accounts {
		copy := a
		copy.Models = append([]string{}, a.Models...)
		accounts = append(accounts, copy)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return struct {
		Configured    bool            `json:"configured"`
		Accounts      []AccountStatus `json:"accounts"`
		Error         string          `json:"error,omitempty"`
		TTL           int             `json:"catalog_ttl_seconds"`
		GitHubBaseURL string          `json:"github_base_url"`
	}{true, accounts, s.syncError, s.Config().ModelCacheTTLSeconds, s.Config().GitHubBaseURL}
}

// SyncAccounts reads host-owned auth records and refreshes local model catalogs.
// It never scans the filesystem or writes credentials. The v8.0.15 host only
// provides an unconditional whole-document save, so even a read-compare-save can
// overwrite a token rotation, disable operation, or other concurrent edit.
//
// Until the host provides an atomic metadata patch or explicit registry refresh,
// background discovery cannot notify its model registry of catalog changes. The
// registry may remain stale until the host next invokes model.for_auth. Local
// catalog refreshes and request-time eligibility checks still fail closed.
func (s *Service) SyncAccounts(ctx context.Context, force bool) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	rpc, ok := s.host.(transport.Caller)
	if !ok {
		return errors.New("host auth callbacks unavailable")
	}
	var list struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := rpc.Call("host.auth.list", struct{}{}, &list); err != nil {
		s.accountsMu.Lock()
		s.syncError = "cannot enumerate host credentials"
		s.accountsMu.Unlock()
		return errors.New("cannot enumerate host credentials")
	}
	live := map[string]bool{}
	generations := map[string]bool{}
	for _, file := range list.Files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if file.Provider != providerID || file.Disabled || file.RuntimeOnly {
			continue
		}
		live[file.ID] = true
		var record struct {
			JSON json.RawMessage `json:"json"`
		}
		selector := map[string]string{"auth_index": file.AuthIndex}
		if rpc.Call("host.auth.get", selector, &record) != nil {
			s.setAccount(AccountStatus{ID: file.ID, Error: "cannot read host credential", Models: []string{}})
			continue
		}
		storage, err := parseStorage(record.JSON)
		if err != nil {
			s.setAccount(AccountStatus{ID: file.ID, Error: "invalid Copilot credential", Models: []string{}})
			continue
		}
		models, token, discoveryErr := s.models(ctx, "", file.ID, storage, force)
		status := AccountStatus{ID: file.ID, Login: storage.GitHubLogin, Models: []string{}, TokenExpiresAt: token.ExpiresAt}
		key := cacheKey(file.ID, storage)
		generations[key] = true
		s.modelMu.Lock()
		entry := s.modelEntries[key]
		s.modelMu.Unlock()
		status.ExpiresAt = entry.ExpiresAt
		if discoveryErr == nil {
			status.LastSuccess = entry.FetchedAt
		} else {
			s.accountsMu.Lock()
			status.LastSuccess = s.accounts[file.ID].LastSuccess
			s.accountsMu.Unlock()
		}
		if discoveryErr != nil {
			status.Error = "account model catalog unavailable"
			models = nil
		}
		infos := modelInfos(models, s.Config())
		for _, model := range infos {
			status.Models = append(status.Models, model.ID)
		}
		status.SyncError = hostCatalogSyncUnavailable
		s.setAccount(status)
	}
	s.accountsMu.Lock()
	s.syncError = ""
	for id := range s.accounts {
		if !live[id] {
			delete(s.accounts, id)
			s.invalidateAuth(id)
		}
	}
	s.accountsMu.Unlock()
	s.purgeMissing(generations)
	// Bound device-flow state even when a client abandons polling.
	s.oauthMu.Lock()
	for id, session := range s.oauthSession {
		if !s.now().Before(session.ExpiresAt) {
			delete(s.oauthSession, id)
		}
	}
	s.oauthMu.Unlock()
	return nil
}
func (s *Service) setAccount(status AccountStatus) {
	s.accountsMu.Lock()
	s.accounts[status.ID] = status
	s.accountsMu.Unlock()
}

// Purge token, retry and catalog entries for credential generations no longer
// referenced by the host. External token rotations do not call invalidateAuth.
func (s *Service) purgeMissing(generations map[string]bool) {
	s.modelMu.Lock()
	for key := range s.modelEntries {
		if !generations[key] {
			delete(s.modelEntries, key)
		}
	}
	s.modelMu.Unlock()
	s.tokenMu.Lock()
	for key := range s.tokenEntries {
		if !generations[key] {
			delete(s.tokenEntries, key)
		}
	}
	for key := range s.tokenRetries {
		if !generations[key] {
			delete(s.tokenRetries, key)
		}
	}
	s.tokenMu.Unlock()
}
