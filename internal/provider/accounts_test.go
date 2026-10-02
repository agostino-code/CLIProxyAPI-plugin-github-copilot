package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"cliproxyapi-github-copilot/internal/transport"
)

type accountHost struct {
	*mockHost
	mu         sync.Mutex
	raw        json.RawMessage
	saves      int
	editOnRead bool
	reads      int
}

func (h *accountHost) Call(method string, in, out any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var value any
	switch method {
	case "host.auth.list":
		value = map[string]any{"files": []pluginapi.HostAuthFileEntry{{ID: "id", AuthIndex: "index", Name: "copilot-test.json", Provider: "copilot"}}}
	case "host.auth.get":
		h.reads++
		if h.editOnRead && h.reads == 2 {
			h.raw = json.RawMessage(`{"type":"copilot","github_access_token":"rotated","github_login":"test","note":"newer"}`)
		}
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
func TestCatalogSynchronizationPreservesAndSkipsUnchanged(t *testing.T) {
	h := &accountHost{raw: json.RawMessage(`{"type":"copilot","github_access_token":"old","github_login":"test","custom":{"nested":true}}`)}
	h.mockHost = &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		if strings.Contains(r.URL, "/copilot_internal/") {
			return reply(`{"token":"t","expires_at":9999999999}`), nil
		}
		return reply(catalog("eligible")), nil
	}}
	s := newTestService(t, h)
	if err := s.SyncAccounts(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.saves != 1 || !strings.Contains(string(h.raw), `"custom":{"nested":true}`) || !strings.Contains(string(h.raw), CatalogRevisionKey) {
		t.Fatalf("incorrect persisted metadata: %s", h.raw)
	}
	if err := s.SyncAccounts(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.saves != 1 {
		t.Fatal("unchanged catalog wrote credentials")
	}
	h.do = func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		return transport.Response{StatusCode: 503}, nil
	}
	if err := s.SyncAccounts(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.saves != 2 {
		t.Fatal("catalog outage did not trigger withdrawal")
	}
	status := s.accounts["id"]
	if len(status.Models) != 0 || status.Error == "" || status.LastSuccess.IsZero() {
		t.Fatalf("incorrect failure status: %+v", status)
	}
}
func TestObservedExternalCredentialEditIsNotOverwritten(t *testing.T) {
	h := &accountHost{editOnRead: true, raw: json.RawMessage(`{"type":"copilot","github_access_token":"old","github_login":"test"}`)}
	h.mockHost = &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		if strings.Contains(r.URL, "/copilot_internal/") {
			return reply(`{"token":"t","expires_at":9999999999}`), nil
		}
		return reply(catalog("eligible")), nil
	}}
	s := newTestService(t, h)
	if err := s.SyncAccounts(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if h.saves != 0 || s.accounts["id"].SyncError == "" {
		t.Fatal("observed edit overwritten")
	}
}
