package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"

	"cliproxyapi-github-copilot/internal/transport"
)

func TestModelNamespaceRoundTrip(t *testing.T) {
	models := []upstreamModel{{ID: "copilot/native-model"}, {ID: "gpt-6-sol"}, {ID: "vendor/model"}}
	infos := modelInfos(models, DefaultConfig())
	for i, model := range models {
		if infos[i].ID != "copilot/"+model.ID || infos[i].Name != infos[i].ID {
			t.Fatalf("incorrect public/native names: %+v", infos[i])
		}
		native, err := DefaultConfig().upstreamModelID(infos[i].ID)
		if err != nil || native != model.ID {
			t.Fatalf("namespace round trip: %q %v", native, err)
		}
	}
	if models[1].ID != "gpt-6-sol" {
		t.Fatal("upstream catalog mutated")
	}
	for _, id := range []string{"gpt-6-sol", "copilot/", "", "other/gpt-6-sol", "Copilot/gpt-6-sol"} {
		if _, err := DefaultConfig().upstreamModelID(id); err == nil {
			t.Fatalf("invalid client name accepted: %q", id)
		}
	}
}

func TestResponseNamespacePreservesUserContent(t *testing.T) {
	input := []byte(`{"model":"gpt-6-sol","message":{"model":"gpt-6-sol"},"response":{"model":"gpt-6-sol"},"content":"gpt-6-sol","arguments":"{\"model\":\"gpt-6-sol\"}","usage":{"total_tokens":9007199254740993}}`)
	output, err := rewriteResponseModel(input, "copilot/gpt-6-sol")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"model", "message.model", "response.model"} {
		if gjson.GetBytes(output, path).String() != "copilot/gpt-6-sol" {
			t.Fatalf("missing namespace at %s", path)
		}
	}
	for _, path := range []string{"content", "arguments", "usage.total_tokens"} {
		if gjson.GetBytes(output, path).Raw != gjson.GetBytes(input, path).Raw {
			t.Fatalf("unrelated payload changed at %s", path)
		}
	}
	again, err := rewriteResponseModel(output, "copilot/gpt-6-sol")
	if err != nil || !bytes.Equal(output, again) {
		t.Fatal("response mapping not idempotent")
	}
}

func TestStreamModelNamespace(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(newline, "\n", "LF"), func(t *testing.T) {
			input := strings.Join([]string{"event: message_start", "id: keep-id", `data: {"type":"message_start",`, `data: "message":{"model":"gpt-6-sol"},"content":"gpt-6-sol"}`, "retry: 1000", "", ""}, newline)
			output, err := rewriteStreamModel([]byte(input), "copilot/gpt-6-sol")
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{"event: message_start", "id: keep-id", "retry: 1000"} {
				if !bytes.Contains(output, []byte(line+newline)) {
					t.Fatalf("metadata changed: %q", output)
				}
			}
			if !bytes.HasSuffix(output, []byte(newline+newline)) {
				t.Fatal("frame terminator lost")
			}
			for _, line := range bytes.Split(output, []byte(newline)) {
				if bytes.HasPrefix(line, []byte("data: ")) {
					payload := bytes.TrimPrefix(line, []byte("data: "))
					if !json.Valid(payload) || gjson.GetBytes(payload, "message.model").String() != "copilot/gpt-6-sol" || gjson.GetBytes(payload, "content").String() != "gpt-6-sol" {
						t.Fatalf("incorrect data: %s", payload)
					}
				}
			}
		})
	}
	for _, frame := range []string{"data: [DONE]\n\n", ": keepalive\n\n", `data: {"delta":"gpt-6-sol"}` + "\n\n"} {
		output, err := rewriteStreamModel([]byte(frame), "copilot/gpt-6-sol")
		if err != nil || string(output) != frame {
			t.Fatalf("unrelated frame changed: %q %v", output, err)
		}
	}
}

func TestBareNamesFailBeforeUpstreamAccess(t *testing.T) {
	s := newTestService(t, &mockHost{do: func(context.Context, string, transport.Request) (transport.Response, error) {
		t.Fatal("invalid model reached upstream")
		return transport.Response{}, nil
	}})
	req := ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{Model: "gpt-6-sol", SourceFormat: "openai-response"}, StreamID: "stream"}
	if _, err := s.Execute(context.Background(), req); err == nil {
		t.Fatal("bare execute accepted")
	}
	if _, err := s.ExecuteStream(context.Background(), req); err == nil {
		t.Fatal("bare stream accepted")
	}
	if _, err := s.CountTokensChecked(context.Background(), req); err == nil {
		t.Fatal("bare token count accepted")
	}
}
