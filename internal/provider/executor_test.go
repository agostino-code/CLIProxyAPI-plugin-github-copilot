package provider

import (
	"cliproxyapi-github-copilot/internal/transport"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestCopilotHeadersUseRecognizedIntegration(t *testing.T) {
	headers := copilotHeaders("test-token", false)

	expected := map[string]string{
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Plugin-Version":  "copilot-chat/0.67.0",
		"Editor-Version":         "vscode/1.139.1",
		"OpenAI-Intent":          "conversation-agent",
		"User-Agent":             "GitHubCopilotChat/0.67.0",
		"X-GitHub-Api-Version":   "2026-08-01",
	}

	for name, want := range expected {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestCountTokensReturnsClaudeInputTokens(t *testing.T) {
	t.Parallel()

	resp, err := (&Service{}).CountTokens(ExecuteRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			SourceFormat:    "claude",
			OriginalRequest: []byte(`{"model":"gpt-5.6-sol","system":"Be concise.","messages":[{"role":"user","content":"hello world"}]}`),
		},
	})
	if err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "input_tokens").Int(); got <= 0 {
		t.Fatalf("input_tokens = %d; response=%s", got, resp.Payload)
	}
}

func TestInferenceAndDiscoveryIntents(t *testing.T) {
	if h := modelHeaders("t"); h.Get("OpenAI-Intent") != "model-access" || h.Get("X-Initiator") != "" {
		t.Fatal("incorrect discovery intent")
	}
	for _, tc := range []struct{ body, initiator string }{
		{`{"input":"hello"}`, "user"},
		{`{"input":[{"type":"function_call_output","output":"OK"}]}`, "agent"},
		{`{"messages":[{"role":"tool","content":"OK"}]}`, "agent"},
		{`{"messages":[{"role":"user","content":[{"type":"tool_result"}]}]}`, "agent"},
		{`{"messages":[{"role":"user","content":[{"type":"tool_result"},{"type":"text","text":"continue"}]}]}`, "user"},
	} {
		if h := inferenceHeaders("t", false, []byte(tc.body)); h.Get("X-Initiator") != tc.initiator {
			t.Fatalf("incorrect initiator for %s", tc.body)
		}
	}
}

type failingStreamHost struct {
	mockHost
	read    bool
	message string
}

func (h *failingStreamHost) ReadStream(context.Context, string) (transport.StreamChunk, error) {
	if h.read {
		return transport.StreamChunk{Done: true}, nil
	}
	h.read = true
	return transport.StreamChunk{Payload: []byte("event: error\ndata: {\"error\":{\"message\":\"private-backend-details\"}}\n\n")}, nil
}
func (h *failingStreamHost) CloseOutput(_ context.Context, _ string, message string) {
	h.message = message
}
func TestStreamErrorsDoNotExposeBackendDetails(t *testing.T) {
	h := &failingStreamHost{}
	s := newTestService(t, h)
	s.pumpStream("out", "/responses", "claude", "model", nil, nil, transport.Stream{ID: "in"})
	if h.message == "" || strings.Contains(h.message, "private-backend-details") {
		t.Fatalf("unsafe stream error %q", h.message)
	}
}
