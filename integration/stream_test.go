package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// Validate the client wire format, not merely whether an OK substring survived.
// In particular data: data: {...} and duplicate [DONE] used to pass integration.
func assertStreamFraming(t *testing.T, route string, body []byte) {
	t.Helper()
	if err := validateStreamFraming(route, body); err != nil {
		t.Fatalf("%s: %v; body=%s", route, err, body)
	}
}

func validateStreamFraming(route string, body []byte) error {
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	var payloads, done int
	terminal, finishedChoice := false, false
	for _, frame := range bytes.Split(body, []byte("\n\n")) {
		var fields [][]byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if len(line) == 0 || line[0] == ':' {
				continue
			}
			name, value, hasColon := bytes.Cut(line, []byte(":"))
			if !hasColon {
				return fmt.Errorf("unframed stream line")
			}
			if bytes.Equal(name, []byte("data")) {
				fields = append(fields, bytes.TrimPrefix(value, []byte(" ")))
			}
		}
		if len(fields) == 0 {
			continue
		}
		data := bytes.Join(fields, []byte("\n"))
		if done > 0 {
			return fmt.Errorf("data after terminal DONE")
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			done++
			continue
		}
		var event struct {
			Type    string          `json:"type"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &event) != nil {
			return fmt.Errorf("invalid SSE JSON payload")
		}
		if event.Type == "error" || event.Type == "response.failed" || len(event.Error) > 0 && !bytes.Equal(event.Error, []byte("null")) {
			return fmt.Errorf("unexpected stream error")
		}
		payloads++
		for _, choice := range event.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishedChoice = true
			}
		}
		if route == "/v1/responses" && (event.Type == "response.completed" || event.Type == "response.incomplete") || route == "/v1/messages" && event.Type == "message_stop" {
			terminal = true
		}
	}
	if payloads == 0 {
		return fmt.Errorf("no JSON events")
	}
	if route == "/v1/chat/completions" {
		if done != 1 || !finishedChoice {
			return fmt.Errorf("chat requires one DONE and a finish_reason")
		}
	} else if done != 0 || !terminal {
		return fmt.Errorf("missing protocol terminal event or unexpected DONE")
	}
	return nil
}

func TestStreamFramingAssertions(t *testing.T) {
	valid := "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	if err := validateStreamFraming("/v1/chat/completions", []byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"data: data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
		valid + "data: [DONE]\n\n",
		"data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
		"data: {\"error\":{\"message\":\"oops\"}}\n\ndata: [DONE]\n\n",
	} {
		if err := validateStreamFraming("/v1/chat/completions", []byte(invalid)); err == nil {
			t.Fatalf("accepted malformed stream: %s", invalid)
		}
	}
}
