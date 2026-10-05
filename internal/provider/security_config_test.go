package provider

import "testing"

func TestPublicEndpointsRequireExplicitTrust(t *testing.T) {
	for _, raw := range []string{
		"github_base_url: https://login.example.test",
		"github_api_url: https://api.example.test",
		"copilot_api_url: https://inference.example.test",
		"github_api_url: https://api.github.com.evil.test",
		"copilot_api_url: https://api.githubcopilot.com.evil.test",
		"github_api_url: https://api.github.com:8443",
		"github_api_url: https://api.github.com/prefix",
		"copilot_api_url: https://api.githubcopilot.com/prefix",
	} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("implicit custom endpoint accepted: %s", raw)
		}
		if _, err := ParseConfig([]byte(raw + "\nallow_custom_endpoints: true")); err != nil {
			t.Errorf("explicit trusted endpoint rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "copilot_api_url: https://api.business.githubcopilot.com", "copilot_api_url: https://api.enterprise.githubcopilot.com"} {
		if _, err := ParseConfig([]byte(raw)); err != nil {
			t.Errorf("trusted public endpoint rejected: %s: %v", raw, err)
		}
	}
}

func TestCustomEndpointOptInDoesNotDisableURLSafety(t *testing.T) {
	for _, raw := range []string{
		"github_api_url: http://api.example.test",
		"github_api_url: https://user:pass@api.example.test",
		"github_api_url: https://api.example.test?token=secret",
		"github_api_url: https://api.example.test?",
		"github_api_url: https://api.example.test.",
		tenantConfig + "copilot_api_url: https://api.githubcopilot.com",
		tenantConfig + "github_api_url: https://api.other.ghe.com",
	} {
		if _, err := ParseConfig([]byte(raw + "\nallow_custom_endpoints: true")); err == nil {
			t.Errorf("unsafe endpoint accepted with opt-in: %s", raw)
		}
	}
}

func TestLoopbackTestEndpointsRemainExplicitlySupported(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080", "https://127.0.0.1:8080"} {
		raw := "github_base_url: " + origin + "\ngithub_api_url: " + origin + "\ncopilot_api_url: " + origin
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("implicit loopback exception: %s", origin)
		}
		if _, err := ParseConfig([]byte(raw + "\nallow_insecure_base_urls: true")); err != nil {
			t.Errorf("explicit loopback rejected: %s %v", origin, err)
		}
	}
}

func TestOAuthScopesRequireExplicitOptIn(t *testing.T) {
	for _, scope := range []string{"repo", "read:user repo", "admin:org", "read:user,user:email"} {
		raw := "github_scope: " + scope
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Errorf("broader scope accepted: %s", scope)
		}
		cfg, err := ParseConfig([]byte(raw + "\nallow_custom_scopes: true"))
		if err != nil || cfg.GitHubScope != scope {
			t.Errorf("explicit scope rejected: %s %v", scope, err)
		}
	}
	for _, scope := range []string{"'read:user'", "'  read:user  '", "''"} {
		if _, err := ParseConfig([]byte("github_scope: " + scope)); err != nil {
			t.Errorf("least-privilege scope rejected: %s %v", scope, err)
		}
	}
}
