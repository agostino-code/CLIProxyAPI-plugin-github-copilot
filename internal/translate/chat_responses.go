package translate

import (
	"context"
	"encoding/json"
	"errors"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The v8 SDK registers Chat-to-Responses under its Codex provider format.
// Reuse its structural conversion but remove Codex-specific defaults and restore
// the generation controls supported by a general Copilot Responses endpoint.
func chatRequestToResponses(model string, body []byte, stream bool) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	converted := registry.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, model, body, stream)
	out, err := decodeObject(converted)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"reasoning", "parallel_tool_calls", "include", "store"} {
		delete(out, key)
	}
	for _, key := range []string{"temperature", "top_p", "parallel_tool_calls", "store", "metadata", "user", "service_tier"} {
		copyField(out, root, key, key)
	}
	if value, ok := root["reasoning_effort"]; ok {
		out["reasoning"] = map[string]any{"effort": value}
	}
	copyField(out, root, "max_tokens", "max_output_tokens")
	copyField(out, root, "max_completion_tokens", "max_output_tokens")
	if _, ok := out["input"]; !ok {
		return nil, errors.New("Chat-to-Responses conversion produced no input")
	}
	return json.Marshal(out)
}
func responsesResponseToChat(ctx context.Context, model string, original, translated, body []byte) ([]byte, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	eventType := "response.completed"
	if root["status"] == "incomplete" {
		eventType = "response.incomplete"
	}
	if root["status"] == "failed" {
		return nil, errors.New("upstream response failed")
	}
	envelope, err := json.Marshal(map[string]any{"type": eventType, "response": root})
	if err != nil {
		return nil, err
	}
	out := registry.TranslateNonStream(ctx, sdktranslator.FormatCodex, sdktranslator.FormatOpenAI, model, original, translated, envelope, nil)
	if !json.Valid(out) {
		return nil, errors.New("responses-to-Chat conversion failed")
	}
	return out, nil
}
