package plugin

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed dashboard.html
var dashboard []byte

func managementRegistration() any {
	base := "/v0/management/plugins/" + ID
	return map[string]any{"routes": []pluginapi.ManagementRoute{{Method: "GET", Path: base + "/status"}, {Method: "POST", Path: base + "/refresh"}}, "resources": []pluginapi.ResourceRoute{{Path: "/dashboard", Menu: "GitHub Copilot", Description: "Account authentication and eligible models"}}}
}
func jsonResponse(status int, value any) pluginapi.ManagementResponse {
	raw, err := json.Marshal(value)
	if err != nil {
		status = 500
		raw = []byte(`{"error":"cannot encode response"}`)
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, Body: raw}
}
func (p *Plugin) manage(raw []byte) (pluginapi.ManagementResponse, error) {
	var req pluginapi.ManagementRequest
	if json.Unmarshal(raw, &req) != nil {
		return jsonResponse(400, map[string]string{"error": "invalid request"}), nil
	}
	if req.Path == "/v0/resource/plugins/"+ID+"/dashboard" && req.Method == "GET" {
		// Inline code is immutable embedded content. Hash-based CSP allows it without unsafe-inline.
		hash := func(tag string) string {
			body := strings.SplitN(string(dashboard), "<"+tag+">", 2)[1]
			body = strings.SplitN(body, "</"+tag+">", 2)[0]
			sum := sha256.Sum256([]byte(body))
			return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		}
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "Referrer-Policy": {"no-referrer"}, "X-Content-Type-Options": {"nosniff"}, "Content-Security-Policy": {fmt.Sprintf("default-src 'none'; script-src %s; style-src %s; connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'", hash("script"), hash("style"))}}, Body: dashboard}, nil
	}
	p.mu.RLock()
	s := p.service
	message := p.configError
	p.mu.RUnlock()
	if s == nil {
		if message == "" {
			message = "plugin disabled or not configured"
		}
		return jsonResponse(503, map[string]any{"configured": false, "error": message}), nil
	}
	switch {
	case req.Method == "GET" && req.Path == "/v0/management/plugins/"+ID+"/status":
		return jsonResponse(200, s.Status()), nil
	case req.Method == "POST" && req.Path == "/v0/management/plugins/"+ID+"/refresh":
		if err := s.SyncAccounts(s.Context(), true); err != nil {
			return jsonResponse(503, map[string]string{"error": "cannot synchronize account catalogs"}), nil
		}
		return jsonResponse(200, s.Status()), nil
	default:
		return jsonResponse(404, map[string]string{"error": "route not found"}), nil
	}
}
