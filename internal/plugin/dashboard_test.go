package plugin

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManagementStatusIncludesConfiguredGitHubOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, config, origin string
	}{
		{"public", "enabled: true", "https://github.com"},
		{"enterprise", "github_base_url: https://example.ghe.com\ngithub_client_id: synthetic-client", "https://example.ghe.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(noHost{})
			defer p.Close()
			dispatch(t, p, "plugin.register", lifecycleRequest{SchemaVersion: 6, ConfigYAML: []byte(tc.config)})
			raw, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/plugins/github-copilot/status"})
			resp, err := p.manage(raw)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("authenticated status unavailable: %d %v", resp.StatusCode, err)
			}
			var status struct {
				Origin string `json:"github_base_url"`
			}
			if err := json.Unmarshal(resp.Body, &status); err != nil || status.Origin != tc.origin {
				t.Fatalf("unexpected configured origin: %q %v", status.Origin, err)
			}
		})
	}
}

func TestDashboardAuthorizationOriginChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to execute the embedded dashboard's URL checks")
	}
	// Execute the actual embedded script. DOM stubs only satisfy registration of
	// event handlers; URL parsing and origin comparison use the real JS runtime.
	_, script, ok := strings.Cut(string(dashboard), "<script>")
	if !ok {
		t.Fatal("dashboard script is missing")
	}
	script, _, ok = strings.Cut(script, "</script>")
	if !ok {
		t.Fatal("dashboard script is incomplete")
	}
	encoded, _ := json.Marshal(script)
	program := `
const assert=require('node:assert/strict'), vm=require('node:vm');
const context={URL,document:{getElementById:()=>({addEventListener(){}})},window:{addEventListener(){}}};
vm.createContext(context);
vm.runInContext(` + string(encoded) + `,context);
const validate=context.authorizationURL;
for(const [origin,url] of [
 ['https://github.com','https://github.com/login/device?user_code=TEST'],
 ['https://example.ghe.com','https://example.ghe.com/login/device?user_code=TEST'],
 ['https://example.ghe.com','https://EXAMPLE.ghe.com/login/device?user_code=TEST'],
]){
 assert.equal(validate({url},origin).href,new URL(url).href);
}
for(const [origin,url] of [
 ['https://example.ghe.com','https://other.ghe.com/login/device'],
 ['https://example.ghe.com','https://github.com/login/device'],
 ['https://github.com','https://example.ghe.com/login/device'],
 ['https://example.ghe.com','https://example.ghe.com.evil.test/login/device'],
 ['https://example.ghe.com','https://example.ghe.com:8443/login/device'],
 ['https://example.ghe.com','http://example.ghe.com/login/device'],
 ['https://example.ghe.com','javascript:alert(1)'],
 ['https://example.ghe.com','https://user:password@example.ghe.com/login/device'],
 ['https://example.ghe.com','https://user@example.ghe.com/login/device'],
 ['https://example.ghe.com','https://example.ghe.com@evil.test/login/device'],
 ['https://example.ghe.com','/login/device'],
 ['https://user:password@example.ghe.com','https://example.ghe.com/login/device'],
 ['http://example.ghe.com','http://example.ghe.com/login/device'],
 [undefined,'https://github.com/login/device'],
]){
 assert.throws(()=>validate({url},origin),undefined,JSON.stringify([origin,url]));
}
const safe='https://example.ghe.com/login/device?user_code=TEST';
assert.equal(validate({url:safe,verification_uri_complete:'https://evil.test/steal',completeURL:'javascript:alert(1)'},'https://example.ghe.com').href,safe);
`
	if output, err := exec.Command(node, "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("dashboard origin validation failed: %v\n%s", err, output)
	}
}
