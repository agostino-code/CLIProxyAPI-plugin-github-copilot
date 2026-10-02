package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"cliproxyapi-github-copilot/internal/transport"
)

const CatalogRevisionKey = "github_copilot_catalog_revision"

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
		Configured bool            `json:"configured"`
		Accounts   []AccountStatus `json:"accounts"`
		Error      string          `json:"error,omitempty"`
		TTL        int             `json:"catalog_ttl_seconds"`
	}{true, accounts, s.syncError, s.Config().ModelCacheTTLSeconds}
}

// SyncAccounts uses only host-owned auth records. It never scans the filesystem.
// A revision marker changes the host watcher's semantic auth snapshot and causes
// model.for_auth to replace (including clear) that account's registry entries.
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
	for _, file := range list.Files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if file.Provider != providerID || file.Disabled || file.RuntimeOnly {
			continue
		}
		live[file.ID] = true
		var record struct {
			Name string          `json:"name"`
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
		encoded, _ := json.Marshal(infos)
		hash := sha256.Sum256(encoded)
		revision := hex.EncodeToString(hash[:])
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(record.JSON, &fields)
		var previous string
		_ = json.Unmarshal(fields[CatalogRevisionKey], &previous)
		if previous != revision {
			// Read again immediately before saving. Never overwrite an observed
			// external edit or token rotation with a previously fetched document.
			var latest struct {
				JSON json.RawMessage `json:"json"`
			}
			var runtimeInfo pluginapi.HostAuthGetRuntimeResponse
			if rpc.Call("host.auth.get_runtime", selector, &runtimeInfo) != nil || runtimeInfo.Auth.Disabled || rpc.Call("host.auth.get", selector, &latest) != nil || !bytes.Equal(latest.JSON, record.JSON) {
				status.SyncError = "credential changed during discovery; retry pending"
			} else {
				fields[CatalogRevisionKey], _ = json.Marshal(revision)
				updated, _ := json.Marshal(fields)
				if rpc.Call("host.auth.save", pluginapi.HostAuthSaveRequest{Name: record.Name, JSON: updated}, nil) != nil {
					status.SyncError = "host catalog synchronization failed"
				}
			}
		}
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
	s.purgeMissing(live)
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

// Purge caches for generations no longer referenced by the host, including
// credentials that never reached the management snapshot.
func (s *Service) purgeMissing(live map[string]bool) {
	s.modelMu.Lock()
	for key := range s.modelEntries {
		id, _, _ := strings.Cut(key, "|")
		if !live[id] {
			delete(s.modelEntries, key)
		}
	}
	s.modelMu.Unlock()
}
