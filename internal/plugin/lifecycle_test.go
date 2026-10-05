package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"cliproxyapi-github-copilot/internal/provider"
	"cliproxyapi-github-copilot/internal/transport"
)

func assertLifecycleSuccess(t *testing.T, p *Plugin, method, config string) {
	t.Helper()
	out := dispatch(t, p, method, lifecycleRequest{SchemaVersion: 6, ConfigYAML: []byte(config)})
	if string(out["ok"]) != "true" {
		t.Fatalf("%s failed: %s", method, out["error"])
	}
}

func assertLifecycleError(t *testing.T, p *Plugin, method, config, code string) {
	t.Helper()
	out := dispatch(t, p, method, lifecycleRequest{SchemaVersion: 6, ConfigYAML: []byte(config)})
	var detail pluginabi.Error
	if err := json.Unmarshal(out["error"], &detail); err != nil {
		t.Fatalf("%s did not return an error: %v (%s)", method, err, out["result"])
	}
	if string(out["ok"]) != "false" || detail.Code != code || detail.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected lifecycle result: ok=%s error=%+v", out["ok"], detail)
	}
}

func TestInvalidInitialConfigurationReportsErrorAndCanRecover(t *testing.T) {
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		t.Run(method, func(t *testing.T) {
			p := New(noHost{})
			defer p.Close()
			assertLifecycleError(t, p, method, "model_cache_ttl_seconds: 9999", "invalid_config")
			if p.service != nil || p.authRouting != nil || len(p.config) != 0 {
				t.Fatal("invalid initial config committed state")
			}
			assertLifecycleSuccess(t, p, method, "enabled: true")
			if p.service == nil || p.configError != "" {
				t.Fatal("valid configuration did not recover after an initial error")
			}
		})
	}
}

func TestInvalidConfigurationPreservesAcceptedService(t *testing.T) {
	for _, config := range []string{
		"[invalid YAML",
		"model_cache_ttl_seconds: 9999",
		"github_base_url: https://example.ghe.com",
		"github_api_url: https://untrusted.example",
		"github_scope: repo",
		"enabled: false\nmodel_cache_ttl_seconds: 9999",
	} {
		for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
			t.Run(method+"/"+config, func(t *testing.T) {
				p := New(noHost{})
				defer p.Close()
				const initial = "enabled: true"
				assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, initial)
				old := p.service
				assertLifecycleError(t, p, method, config, "invalid_config")
				if p.service != old || old.Context().Err() != nil || string(p.config) != initial || !p.authRouting.SameAuthRouting(old.Config()) {
					t.Fatal("rejected configuration changed the accepted service or config")
				}
				assertLifecycleSuccess(t, p, method, initial)
				if p.service != old || p.configError != "" {
					t.Fatal("restoring accepted config did not clear the error without replacing the service")
				}
			})
		}
	}
}

func TestLifecycleRejectsAuthenticationRoutingChanges(t *testing.T) {
	const tenant = "github_base_url: https://example.ghe.com\ngithub_client_id: synthetic-client"
	for _, tc := range []struct {
		name, initial, replacement string
	}{
		{"public to enterprise", "", tenant},
		{"enterprise to public", tenant, ""},
		{"enterprise tenant", tenant, strings.ReplaceAll(tenant, "example.ghe.com", "other.ghe.com")},
		{"client identifier", "", "github_client_id: synthetic-other-client"},
		{"insecure opt in", "", "allow_insecure_base_urls: true"},
		{"custom endpoint opt in", "", "allow_custom_endpoints: true"},
		{"custom scope opt in", "", "allow_custom_scopes: true"},
		{"scope change", "allow_custom_scopes: true", "allow_custom_scopes: true\ngithub_scope: repo"},
		{"web endpoint", "allow_custom_endpoints: true", "allow_custom_endpoints: true\ngithub_base_url: https://oauth.example"},
		{"identity endpoint", "allow_custom_endpoints: true", "allow_custom_endpoints: true\ngithub_api_url: https://identity.example"},
		{"copilot endpoint", "allow_custom_endpoints: true", "allow_custom_endpoints: true\ncopilot_api_url: https://copilot.example"},
		{"enterprise identity endpoint", tenant, tenant + "\ngithub_api_url: https://identity.example.ghe.com"},
		{"enterprise copilot endpoint", tenant, tenant + "\ncopilot_api_url: https://copilot.example.ghe.com"},
	} {
		for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				p := New(noHost{})
				defer p.Close()
				assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, tc.initial)
				old := p.service
				assertLifecycleError(t, p, method, tc.replacement, "restart_required")
				if p.service != old || old.Context().Err() != nil || string(p.config) != tc.initial {
					t.Fatal("routing rejection replaced the working service")
				}
			})
		}
	}
}

func TestDisabledConfigurationCannotBypassRoutingGuard(t *testing.T) {
	const tenant = "github_base_url: https://example.ghe.com\ngithub_client_id: synthetic-client\n"
	for _, initiallyEnabled := range []bool{false, true} {
		t.Run(fmt.Sprint(initiallyEnabled), func(t *testing.T) {
			p := New(noHost{})
			defer p.Close()
			assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, tenant+fmt.Sprintf("enabled: %t", initiallyEnabled))
			old := p.service
			assertLifecycleSuccess(t, p, pluginabi.MethodPluginReconfigure, tenant+"enabled: false")
			assertLifecycleSuccess(t, p, pluginabi.MethodPluginReconfigure, tenant+"enabled: false")
			if p.service != nil || old != nil && old.Context().Err() == nil {
				t.Fatal("disabled config left an active service")
			}
			for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
				assertLifecycleError(t, p, method, "enabled: false", "restart_required")
				assertLifecycleError(t, p, method, "enabled: true", "restart_required")
				assertLifecycleError(t, p, method, strings.ReplaceAll(tenant, "example.ghe.com", "other.ghe.com")+"enabled: true", "restart_required")
			}
			assertLifecycleError(t, p, pluginabi.MethodPluginReconfigure, tenant+"enabled: false\nmodel_cache_ttl_seconds: 9999", "invalid_config")
			if p.service != nil || string(p.config) != tenant+"enabled: false" {
				t.Fatal("rejected disabled config changed the accepted state")
			}
			assertLifecycleSuccess(t, p, pluginabi.MethodPluginReconfigure, tenant+"enabled: true")
			if p.service == nil || p.service == old || p.service.Config().GitHubBaseURL != "https://example.ghe.com" {
				t.Fatal("same-routing configuration did not re-enable the service")
			}
		})
	}
}

func TestLifecycleAcceptsNonAuthenticationChanges(t *testing.T) {
	p := New(noHost{})
	defer p.Close()
	assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, "enabled: true")
	old := p.service
	assertLifecycleSuccess(t, p, pluginabi.MethodPluginReconfigure, "enabled: true\nmodel_prefix: team\nmodel_cache_ttl_seconds: 30")
	if p.service == nil || p.service == old || old.Context().Err() == nil || p.service.Config().ModelPrefix != "team" {
		t.Fatal("valid model configuration did not replace the service")
	}
}

func TestShutdownCannotBeUsedAsConfigurationRestart(t *testing.T) {
	p := New(noHost{})
	assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, "enabled: true")
	p.Close()
	defer p.Close()
	out := dispatch(t, p, pluginabi.MethodPluginReconfigure, lifecycleRequest{SchemaVersion: 6, ConfigYAML: []byte("github_client_id: synthetic-new-client")})
	if string(out["ok"]) != "false" || p.service != nil {
		t.Fatal("shut down plugin was reopened by a lifecycle request")
	}
}

// lifecycleHost exercises the real plugin transport with synthetic OAuth data.
// It never sends requests to the public origins used in these routing tests.
type lifecycleHost struct {
	mu      sync.Mutex
	streams map[string][]byte
	urls    []string
	bodies  [][]byte
}

func copyLifecycleJSON(out, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (h *lifecycleHost) Call(method string, in, out any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch method {
	case "host.http.operation_open":
		return copyLifecycleJSON(out, map[string]string{"operation_id": "synthetic-operation"})
	case "host.http.do_stream":
		var request struct {
			URL  string `json:"url"`
			Body []byte `json:"body"`
		}
		if err := copyLifecycleJSON(&request, in); err != nil {
			return err
		}
		var body []byte
		switch request.URL {
		case "https://github.com/login/device/code":
			body = []byte(`{"device_code":"synthetic-device","user_code":"SYNTHETIC","verification_uri":"https://github.com/login/device","expires_in":600,"interval":5}`)
		case "https://github.com/login/oauth/access_token":
			body = []byte(`{"error":"authorization_pending"}`)
		default:
			return fmt.Errorf("unexpected synthetic request URL: %s", request.URL)
		}
		h.urls = append(h.urls, request.URL)
		h.bodies = append(h.bodies, bytes.Clone(request.Body))
		id := fmt.Sprintf("stream-%d", len(h.urls))
		if h.streams == nil {
			h.streams = make(map[string][]byte)
		}
		h.streams[id] = body
		return copyLifecycleJSON(out, map[string]any{"status_code": 200, "stream_id": id})
	case "host.http.stream_read":
		var request struct {
			ID string `json:"stream_id"`
		}
		if err := copyLifecycleJSON(&request, in); err != nil {
			return err
		}
		return copyLifecycleJSON(out, transport.StreamChunk{Payload: h.streams[request.ID], Done: true})
	case "host.http.stream_close", "host.http.cancel":
		return nil
	case "host.auth.list":
		return copyLifecycleJSON(out, map[string]any{"files": []any{}})
	default:
		return fmt.Errorf("unexpected synthetic host callback: %s", method)
	}
}

func TestRejectedReconfigurationPreservesPendingDeviceFlow(t *testing.T) {
	for _, tc := range []struct{ name, config, code string }{
		{"invalid config", "model_cache_ttl_seconds: 9999", "invalid_config"},
		{"different tenant", "github_base_url: https://example.ghe.com\ngithub_client_id: synthetic-client", "restart_required"},
		{"different client", "github_client_id: synthetic-other-client", "restart_required"},
		{"trust change", "allow_custom_endpoints: true", "restart_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &lifecycleHost{}
			p := New(host)
			defer p.Close()
			assertLifecycleSuccess(t, p, pluginabi.MethodPluginRegister, "enabled: true")
			out := dispatch(t, p, pluginabi.MethodAuthLoginStart, rpcAuthLoginStartRequest{})
			var started pluginapi.AuthLoginStartResponse
			if string(out["ok"]) != "true" || json.Unmarshal(out["result"], &started) != nil || started.State == "" {
				t.Fatalf("synthetic device flow failed to start: %s", out["error"])
			}
			old := p.service
			assertLifecycleError(t, p, pluginabi.MethodPluginReconfigure, tc.config, tc.code)
			if p.service != old || old.Context().Err() != nil {
				t.Fatal("rejected reload stopped the pending flow's service")
			}
			out = dispatch(t, p, pluginabi.MethodAuthLoginPoll, rpcAuthLoginPollRequest{AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{State: started.State}})
			var poll pluginapi.AuthLoginPollResponse
			if string(out["ok"]) != "true" || json.Unmarshal(out["result"], &poll) != nil || poll.Status != pluginapi.AuthLoginStatusPending {
				t.Fatalf("pending device flow was lost: %s %s", out["result"], out["error"])
			}
			host.mu.Lock()
			defer host.mu.Unlock()
			if len(host.urls) != 2 || host.urls[1] != "https://github.com/login/oauth/access_token" {
				t.Fatalf("pending flow did not poll the original endpoint: %v", host.urls)
			}
			form, err := url.ParseQuery(string(host.bodies[1]))
			if err != nil || form.Get("device_code") != "synthetic-device" || form.Get("client_id") != provider.DefaultGitHubClientID {
				t.Fatalf("pending flow used different OAuth inputs: %v", err)
			}
		})
	}
}
