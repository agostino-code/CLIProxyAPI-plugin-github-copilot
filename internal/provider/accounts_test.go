package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"cliproxyapi-github-copilot/internal/transport"
)

type accountHost struct {
	*mockHost
	mu    sync.Mutex
	raw   json.RawMessage
	saves int
	calls []string
}

func (h *accountHost) Call(method string, in, out any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, method)
	var value any
	switch method {
	case "host.auth.list":
		value = map[string]any{"files": []pluginapi.HostAuthFileEntry{{ID: "id", AuthIndex: "index", Name: "copilot-test.json", Provider: "copilot"}}}
	case "host.auth.get":
		value = map[string]any{"name": "copilot-test.json", "json": h.raw}
	case "host.auth.get_runtime":
		value = pluginapi.HostAuthGetRuntimeResponse{Auth: pluginapi.HostAuthFileEntry{ID: "id", Provider: "copilot"}}
	case "host.auth.save":
		req := in.(pluginapi.HostAuthSaveRequest)
		h.raw = append([]byte(nil), req.JSON...)
		h.saves++
		return nil
	default:
		return errors.New("unsupported")
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func assertReadOnlyAccountSync(t *testing.T, h *accountHost, want json.RawMessage) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.saves != 0 || !bytes.Equal(h.raw, want) {
		t.Fatalf("catalog sync changed host credentials: saves=%d", h.saves)
	}
	for _, method := range h.calls {
		if method != "host.auth.list" && method != "host.auth.get" {
			t.Fatalf("catalog sync used unexpected host callback %q", method)
		}
	}
}

func TestCatalogSynchronizationIsReadOnlyAndRefreshesLocalModels(t *testing.T) {
	raw := json.RawMessage(`{"type":"copilot","github_access_token":"old","github_login":"test","custom":{"nested":true},"github_copilot_catalog_revision":"legacy-revision"}`)
	modelID := "eligible"
	h := &accountHost{raw: append(json.RawMessage(nil), raw...)}
	h.mockHost = &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		if strings.Contains(r.URL, "/copilot_internal/") {
			return reply(`{"token":"t","expires_at":9999999999}`), nil
		}
		return reply(catalog(modelID)), nil
	}}
	s := newTestService(t, h)
	ctx := context.Background()
	storage, err := parseStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"eligible", "eligible", "replacement"} {
		modelID = id
		if err := s.SyncAccounts(ctx, true); err != nil {
			t.Fatal(err)
		}
		assertReadOnlyAccountSync(t, h, raw)
		status := s.accounts["id"]
		if len(status.Models) != 1 || status.Models[0] != s.Config().exposedModelID(id) || status.Error != "" || status.LastSuccess.IsZero() || status.ExpiresAt.IsZero() {
			t.Fatalf("incorrect refreshed status: %+v", status)
		}
		if status.SyncError != hostCatalogSyncUnavailable {
			t.Fatalf("host registry limitation was not reported: %q", status.SyncError)
		}
		if _, _, err := s.endpointForModel(ctx, "", "id", storage, id); err != nil {
			t.Fatalf("refreshed catalog cannot serve %q: %v", id, err)
		}
		response, err := s.ModelsForAuth(ctx, "", pluginapi.AuthModelRequest{AuthID: "id", StorageJSON: raw})
		if err != nil || len(response.Models) != 1 || response.Models[0].ID != s.Config().exposedModelID(id) {
			t.Fatalf("model callback did not return refreshed catalog: %+v, %v", response, err)
		}
	}
	if _, _, err := s.endpointForModel(ctx, "", "id", storage, "eligible"); err == nil {
		t.Fatal("removed model remains eligible for inference")
	}
	lastSuccess := s.accounts["id"].LastSuccess
	h.do = func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		return transport.Response{StatusCode: 503}, nil
	}
	if err := s.SyncAccounts(ctx, true); err != nil {
		t.Fatal(err)
	}
	assertReadOnlyAccountSync(t, h, raw)
	status := s.accounts["id"]
	if len(status.Models) != 0 || status.Error == "" || !status.LastSuccess.Equal(lastSuccess) || status.SyncError != hostCatalogSyncUnavailable {
		t.Fatalf("incorrect failure status: %+v", status)
	}
	if _, _, err := s.endpointForModel(ctx, "", "id", storage, "replacement"); err == nil {
		t.Fatal("failed refresh did not fail closed for inference")
	}
	response, err := s.ModelsForAuth(ctx, "", pluginapi.AuthModelRequest{AuthID: "id", StorageJSON: raw})
	if err != nil || len(response.Models) != 0 {
		t.Fatalf("model callback did not withdraw unavailable models: %+v, %v", response, err)
	}
}

func TestConcurrentCredentialEditIsNeverOverwrittenByCatalogSync(t *testing.T) {
	for _, update := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"token rotation", json.RawMessage(`{"type":"copilot","github_access_token":"rotated","github_login":"test","note":"newer"}`)},
		{"disable account", json.RawMessage(`{"type":"copilot","github_access_token":"old","github_login":"test","disabled":true}`)},
	} {
		t.Run(update.name, func(t *testing.T) {
			h := &accountHost{raw: json.RawMessage(`{"type":"copilot","github_access_token":"old","github_login":"test"}`)}
			h.mockHost = &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
				if strings.Contains(r.URL, "/copilot_internal/") {
					return reply(`{"token":"t","expires_at":9999999999}`), nil
				}
				// Simulate a host edit while discovery is in flight, after the
				// credential snapshot was read. No plugin mutex can protect it.
				h.mu.Lock()
				h.raw = append(json.RawMessage(nil), update.raw...)
				h.mu.Unlock()
				return reply(catalog("eligible")), nil
			}}
			s := newTestService(t, h)
			if err := s.SyncAccounts(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			assertReadOnlyAccountSync(t, h, update.raw)
		})
	}
}

func TestPurgeCachesForRotatedCredentialGenerations(t *testing.T) {
	s := newTestService(t, &mockHost{})
	old := cacheKey("same-account", authStorage{GitHubAccessToken: "old-synthetic"})
	current := cacheKey("same-account", authStorage{GitHubAccessToken: "new-synthetic"})
	for _, key := range []string{old, current} {
		s.tokenEntries[key] = copilotTokenEntry{Token: "synthetic"}
		s.tokenRetries[key] = time.Now()
		s.modelEntries[key] = modelCacheEntry{}
	}
	s.purgeMissing(map[string]bool{current: true})
	if len(s.tokenEntries) != 1 || len(s.tokenRetries) != 1 || len(s.modelEntries) != 1 {
		t.Fatal("obsolete credential generations retained")
	}
	if _, ok := s.tokenEntries[current]; !ok {
		t.Fatal("current token removed")
	}
	if _, ok := s.tokenRetries[current]; !ok {
		t.Fatal("current retry removed")
	}
	if _, ok := s.modelEntries[current]; !ok {
		t.Fatal("current catalog removed")
	}
}
