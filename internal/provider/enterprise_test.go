package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"cliproxyapi-github-copilot/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const tenantConfig = "github_base_url: https://example.ghe.com\ngithub_client_id: tenant-device-client\n"

func TestEnterpriseConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(tenantConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAPIURL != "https://api.example.ghe.com" || cfg.CopilotAPIURL != "https://copilot-api.example.ghe.com" {
		t.Fatalf("incorrect tenant endpoints: %#v", cfg)
	}
	for _, raw := range []string{
		"github_base_url: https://example.ghe.com\n",
		"github_base_url: https://example.ghe.com\ngithub_client_id: null\n",
		tenantConfig + "github_api_url: https://api.github.com\n",
		tenantConfig + "copilot_api_url: https://api.githubcopilot.com\n",
		tenantConfig + "copilot_api_url: https://copilot-api.other.ghe.com\n",
		tenantConfig + "copilot_api_url: https://copilot-api.example.ghe.com.evil.test\n",
		tenantConfig + "copilot_api_url: https://copilot-api.example.ghe.com:443\n",
		tenantConfig + "copilot_api_url: https://copilot-api.example.ghe.com/path\n",
		tenantConfig + "copilot_api_url: https://copilot-api.example.ghe.com?\n",
		"github_base_url: https://nested.example.ghe.com\ngithub_client_id: x\n",
		"github_base_url: https://example.ghe.com:443\ngithub_client_id: x\n",
		"github_base_url: https://example.ghe.com/enterprises/example\ngithub_client_id: x\n",
		"github_api_url: https://api.example.ghe.com\n",
		"github_api_url: https://api.other.ghe.com.\n",
		"github_base_url: https://example.ghe.com.\n",
	} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("accepted unsafe tenant config %q", raw)
		}
	}
	public, err := ParseConfig(nil)
	if err != nil || public.GitHubBaseURL != defaultGitHubBaseURL || public.GitHubAPIURL != defaultGitHubAPIURL || public.CopilotAPIURL != defaultCopilotAPIURL {
		t.Fatalf("public defaults changed: %#v %v", public, err)
	}
}

func TestEnterpriseTokenEndpointIsolation(t *testing.T) {
	cfg, err := ParseConfig([]byte(tenantConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range []string{"", "https://copilot-api.example.ghe.com"} {
		got, err := copilotAPIBase(map[string]string{"api": api}, cfg)
		if err != nil || got != cfg.CopilotAPIURL {
			t.Fatalf("tenant endpoint %q: %q %v", api, got, err)
		}
	}
	for _, api := range []string{"https://api.githubcopilot.com", "https://api.enterprise.githubcopilot.com", "https://copilot-api.other.ghe.com", "https://copilot-api.example.ghe.com.evil.test", "https://other.example.ghe.com", "http://copilot-api.example.ghe.com", "https://copilot-api.example.ghe.com:443", "https://copilot-api.example.ghe.com/path", "https://user@copilot-api.example.ghe.com", "https://copilot-api.example.ghe.com?"} {
		if _, err := copilotAPIBase(map[string]string{"api": api}, cfg); err == nil {
			t.Errorf("accepted %q", api)
		}
	}
	if _, err := copilotAPIBase(map[string]string{"api": "https://api.business.githubcopilot.com"}, DefaultConfig()); err != nil {
		t.Fatalf("public routing regressed: %v", err)
	}
}

func TestEnterpriseCredentialIsolation(t *testing.T) {
	calls := 0
	s := newTestService(t, &mockHost{do: func(context.Context, string, transport.Request) (transport.Response, error) {
		calls++
		return transport.Response{}, fmt.Errorf("unexpected network")
	}})
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"", "https://github.com", "https://other.ghe.com"} {
		storage := authStorage{Type: providerID, GitHubAccessToken: "synthetic", GitHubRefreshToken: "synthetic-refresh", GitHubBaseURL: origin, ExpiresAt: 1}
		if _, err := s.copilotToken(context.Background(), "", "id", storage); err == nil {
			t.Errorf("token accepted origin %q", origin)
		}
		if _, _, err := s.models(context.Background(), "", "id", storage, false); err == nil {
			t.Errorf("catalog accepted origin %q", origin)
		}
		raw, _ := marshalStorage(storage)
		if _, err := s.RefreshAuth(context.Background(), "", pluginapi.AuthRefreshRequest{StorageJSON: raw}); err == nil {
			t.Errorf("refresh accepted origin %q", origin)
		}
	}
	if calls != 0 {
		t.Fatalf("rejected credentials caused %d requests", calls)
	}
	public := DefaultConfig()
	if err := public.validateStorageOrigin(authStorage{}); err != nil {
		t.Fatal("legacy public credentials rejected")
	}
	if err := public.validateStorageOrigin(authStorage{GitHubBaseURL: "https://example.ghe.com"}); err == nil {
		t.Fatal("tenant credentials accepted by public configuration")
	}
	a, _ := authData(authStorage{GitHubLogin: "same", GitHubBaseURL: "https://example.ghe.com"}, "", "", "", "", false, nil, nil)
	b, _ := authData(authStorage{GitHubLogin: "same", GitHubBaseURL: "https://other.ghe.com"}, "", "", "", "", false, nil, nil)
	if a.FileName == b.FileName || a.FileName == credentialFileName("same") {
		t.Fatal("credential names collide across origins")
	}
}

func TestEnterpriseOAuthCatalogInferenceRefresh(t *testing.T) {
	var visited []string
	now := time.Now()
	h := &mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		u, err := url.Parse(r.URL)
		if err != nil {
			return transport.Response{}, err
		}
		if u.Host != "example.ghe.com" && u.Host != "api.example.ghe.com" && u.Host != "copilot-api.example.ghe.com" {
			return transport.Response{}, fmt.Errorf("unexpected public or foreign request: %s", r.URL)
		}
		visited = append(visited, r.URL)
		switch r.URL {
		case "https://example.ghe.com/login/device/code":
			return reply(`{"device_code":"synthetic-device","user_code":"TEST-CODE","verification_uri":"https://example.ghe.com/login/device","expires_in":900}`), nil
		case "https://example.ghe.com/login/oauth/access_token":
			form, _ := url.ParseQuery(string(r.Body))
			if form.Get("client_id") != "tenant-device-client" {
				t.Errorf("incorrect OAuth client")
			}
			return reply(`{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expires_in":3600}`), nil
		case "https://api.example.ghe.com/user":
			return reply(`{"login":"test-user","id":123}`), nil
		case "https://api.example.ghe.com/copilot_internal/v2/token":
			return tokenReply(now, "synthetic-copilot"), nil
		case "https://copilot-api.example.ghe.com/models":
			return reply(`{"data":[{"id":"test-model","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]}]}`), nil
		case "https://copilot-api.example.ghe.com/chat/completions":
			return reply(`{"id":"test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`), nil
		}
		return transport.Response{}, fmt.Errorf("unexpected endpoint %s", r.URL)
	}}
	s := newTestService(t, h)
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartLogin(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(start.URL, "https://example.ghe.com/login/device?") {
		t.Fatal(start.URL)
	}
	login, err := s.PollLogin(context.Background(), "", start.State)
	if err != nil || login.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("login: %#v %v", login, err)
	}
	storage, err := parseStorage(login.Auth.StorageJSON)
	if err != nil || storage.GitHubBaseURL != "https://example.ghe.com" {
		t.Fatalf("missing credential binding: %v", err)
	}
	req := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: login.Auth.ID, StorageJSON: login.Auth.StorageJSON, Model: "copilot/test-model", SourceFormat: "openai", Payload: []byte(`{"messages":[{"role":"user","content":"synthetic test"}]}`)}}
	response, err := s.Execute(context.Background(), req)
	if err != nil || !json.Valid(response.Payload) {
		t.Fatalf("inference: %s %v", response.Payload, err)
	}
	storage.ExpiresAt = now.Add(time.Minute).Unix()
	raw, _ := marshalStorage(storage)
	refresh, err := s.RefreshAuth(context.Background(), "", pluginapi.AuthRefreshRequest{AuthID: login.Auth.ID, StorageJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := parseStorage(refresh.Auth.StorageJSON)
	if err != nil || refreshed.GitHubBaseURL != storage.GitHubBaseURL {
		t.Fatal("refresh lost origin")
	}
	if len(visited) != 7 {
		t.Fatalf("expected 7 tenant-only requests, got %v", visited)
	}
}

func TestRoutingChangesRequireRestart(t *testing.T) {
	s := newTestService(t, &mockHost{})
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	if err := s.Configure([]byte(tenantConfig + "model_cache_ttl_seconds: 30\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(nil); err == nil {
		t.Fatal("allowed tenant-to-public hot switch")
	}
	if s.Config().enterpriseHost() != "example.ghe.com" {
		t.Fatal("rejected config changed routing")
	}
}

func TestInvalidVerificationDoesNotOccupySession(t *testing.T) {
	s := newTestService(t, &mockHost{do: func(context.Context, string, transport.Request) (transport.Response, error) {
		return reply(`{"device_code":"synthetic","user_code":"TEST","verification_uri":"https://other.ghe.com/login/device"}`), nil
	}})
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		if _, err := s.StartLogin(context.Background(), ""); err == nil {
			t.Fatal("accepted foreign verification")
		}
	}
	if len(s.oauthSession) != 0 {
		t.Fatal("invalid verification exhausted pending login slots")
	}
}

type tenantRetryStreamHost struct {
	mockHost
	requests []transport.Request
}

func (h *tenantRetryStreamHost) OpenStream(_ context.Context, _ string, r transport.Request) (transport.Stream, error) {
	h.requests = append(h.requests, r)
	status := 200
	if len(h.requests) == 1 {
		status = 401
	}
	return transport.Stream{ID: fmt.Sprint(len(h.requests)), StatusCode: status}, nil
}
func TestEnterpriseStreamReauthorizationStaysInTenant(t *testing.T) {
	exchanges := 0
	h := &tenantRetryStreamHost{mockHost: mockHost{do: func(_ context.Context, _ string, r transport.Request) (transport.Response, error) {
		exchanges++
		if r.URL != "https://api.example.ghe.com/copilot_internal/v2/token" || r.Headers.Get("Authorization") != "token synthetic-access" {
			return transport.Response{}, fmt.Errorf("incorrect tenant exchange")
		}
		return tokenReply(time.Now(), "synthetic-new-copilot"), nil
	}}}
	s := newTestService(t, h)
	if err := s.Configure([]byte(tenantConfig)); err != nil {
		t.Fatal(err)
	}
	storage := authStorage{GitHubAccessToken: "synthetic-access", GitHubBaseURL: "https://example.ghe.com"}
	token := copilotTokenEntry{Token: "synthetic-expired", APIBaseURL: s.Config().CopilotAPIURL}
	stream, _, err := s.openModelStream(context.Background(), "", "account", storage, token, "/chat/completions", []byte(`{"messages":[]}`))
	if err != nil || stream.StatusCode != 200 || exchanges != 1 || len(h.requests) != 2 {
		t.Fatalf("stream retry: %#v %v exchanges=%d", stream, err, exchanges)
	}
	for _, r := range h.requests {
		if r.URL != "https://copilot-api.example.ghe.com/chat/completions" {
			t.Fatalf("stream escaped tenant: %s", r.URL)
		}
	}
	if h.requests[1].Headers.Get("Authorization") != "Bearer synthetic-new-copilot" {
		t.Fatal("retried stream used rejected token")
	}
}
