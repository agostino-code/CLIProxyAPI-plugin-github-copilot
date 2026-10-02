package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeMessagesContextManagement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		context     string
		wantContext bool
	}{
		{"keep all", `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`, false},
		{"keep one", `{"edits":[{"type":"clear_thinking_20251015","keep":1}]}`, true},
		{"additional edit", `{"edits":[{"type":"clear_thinking_20251015","keep":"all"},{"type":"clear_tool_uses_20250919"}]}`, true},
		{"additional context field", `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}],"mode":"custom"}`, true},
		{"additional edit field", `{"edits":[{"type":"clear_thinking_20251015","keep":"all","extra":true}]}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, stream := range []bool{false, true} {
				original := []byte(`{"model":"copilot/claude-sonnet-5.5","max_tokens":64,"messages":[{"role":"user","content":"OK"}],"context_management":` + tc.context + `}`)
				out, err := RequestForEndpointFrom("claude", "claude-sonnet-5.5", original, stream, EndpointMessages)
				if err != nil {
					t.Fatal(err)
				}
				if got := gjson.GetBytes(out, "context_management").Exists(); got != tc.wantContext {
					t.Fatalf("stream=%v context management present=%v, want %v: %s", stream, got, tc.wantContext, out)
				}
				if got := gjson.GetBytes(out, "model").String(); got != "claude-sonnet-5.5" {
					t.Fatalf("model = %q; request=%s", got, out)
				}
				if gjson.GetBytes(out, "stream").Bool() != stream || gjson.GetBytes(out, "messages.0.content").String() != "OK" {
					t.Fatalf("request changed beyond context management: %s", out)
				}
			}
		})
	}
}

func TestClaudeRequestToResponsesPreservesCoreContent(t *testing.T) {
	t.Parallel()

	claudeRequest := []byte(`{
		"model":"ignored",
		"max_tokens":321,
		"system":"Be concise.",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"describe this"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}
			]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"inspect first","signature":"sig"},
				{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"found"}]}
		],
		"tools":[{"name":"lookup","description":"Look up data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
	}`)

	out, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", claudeRequest, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate Claude request: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q", got)
	}
	if got := data.Get("max_output_tokens").Int(); got != 321 {
		t.Fatalf("max_output_tokens = %d", got)
	}
	text := string(out)
	for _, needle := range []string{
		"Be concise.",
		"describe this",
		"data:image/png;base64,YWJj",
		"inspect first",
		`"type":"function_call"`,
		`"call_id":"call_1"`,
		`"type":"function_call_output"`,
		`"name":"lookup"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("translated request omits %q: %s", needle, out)
		}
	}
}

func TestClaudeStructuredOutputAddsResponsesSchemaName(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"model":"gpt-5.6-terra",
		"messages":[{"role":"user","content":"Create a title."}],
		"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}
	}`)
	out, err := RequestForEndpointFrom("claude", "gpt-5.6-terra", original, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	if got := gjson.GetBytes(out, "text.format.name").String(); got != "claude_structured_output" {
		t.Fatalf("text.format.name = %q; request=%s", got, out)
	}
}

func TestChatNonStreamResponseToResponses(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	translated, err := RequestForEndpoint("gpt-test", original, false, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	upstream := []byte(`{
		"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-test",
		"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
	}`)
	out, err := ResponseToResponses(context.Background(), EndpointChatCompletions, "gpt-test", original, translated, upstream)
	if err != nil {
		t.Fatalf("translate response: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("status").String(); got != "completed" {
		t.Fatalf("status = %q; response=%s", got, out)
	}
	if got := data.Get("output.0.content.0.text").String(); got != "pong" {
		t.Fatalf("output text = %q; response=%s", got, out)
	}
	if got := data.Get("usage.total_tokens").Int(); got != 3 {
		t.Fatalf("total tokens = %d; response=%s", got, out)
	}
}

func TestResponsesNonStreamResponseToClaude(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-5.6-sol","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`)
	translated, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", original, false, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	upstream := []byte(`{
		"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-5.6-sol",
		"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong","annotations":[]}]}],
		"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
	}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-5.6-sol", original, translated, upstream)
	if err != nil {
		t.Fatalf("translate response: %v", err)
	}
	data := gjson.ParseBytes(out)
	if got := data.Get("type").String(); got != "message" {
		t.Fatalf("Claude response type = %q; response=%s", got, out)
	}
	if got := data.Get("content.0.text").String(); got != "pong" {
		t.Fatalf("Claude response text = %q; response=%s", got, out)
	}
	if got := data.Get("usage.input_tokens").Int(); got != 2 {
		t.Fatalf("Claude input tokens = %d; response=%s", got, out)
	}
}

func TestChatSSEToResponses(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	translated, err := RequestForEndpoint("gpt-test", original, true, EndpointChatCompletions)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"po"},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"ng"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`),
		[]byte(`data: [DONE]`),
	}
	var state any
	var output strings.Builder
	for _, chunk := range chunks {
		frames, errTranslate := StreamToResponses(context.Background(), EndpointChatCompletions, "gpt-test", original, translated, chunk, &state)
		if errTranslate != nil {
			t.Fatalf("translate SSE: %v", errTranslate)
		}
		for _, frame := range frames {
			output.Write(frame)
		}
	}
	text := output.String()
	for _, needle := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"po"`,
		`"delta":"ng"`,
		"event: response.completed",
		`"total_tokens":3`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("translated SSE omits %q:\n%s", needle, text)
		}
	}
}

func TestResponsesSSEToClaude(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"gpt-5.6-sol","max_tokens":20,"messages":[{"role":"user","content":"ping"}]}`)
	translated, err := RequestForEndpointFrom("claude", "gpt-5.6-sol", original, true, EndpointResponses)
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	chunks := [][]byte{
		[]byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":2,"output_tokens":0}}}

`),
		[]byte(`event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"one"}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" two"}

`),
		[]byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" three"}

`),
		[]byte(`event: response.content_part.done
data: {"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"one two three"}}

`),
		[]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"one two three"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}

`),
	}
	var state any
	var output strings.Builder
	for _, chunk := range chunks {
		frames, errTranslate := StreamFromEndpoint(context.Background(), EndpointResponses, "claude", "gpt-5.6-sol", original, translated, chunk, &state)
		if errTranslate != nil {
			t.Fatalf("translate SSE: %v", errTranslate)
		}
		for _, frame := range frames {
			output.Write(frame)
		}
	}
	text := output.String()
	for _, needle := range []string{
		"event: message_start",
		"event: content_block_start",
		`"type":"text"`,
		`"text":"one"`,
		`"text":" two"`,
		`"text":" three"`,
		`"stop_reason":"end_turn"`,
		`"output_tokens":3`,
		"event: message_stop",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("Claude SSE omits %q:\n%s", needle, text)
		}
	}
}

func TestChatResponsesSDKAdapterPreservesControlsAndFrames(t *testing.T) {
	original := []byte(`{"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":32,"temperature":0.4,"parallel_tool_calls":false}`)
	request, err := RequestForEndpointFrom("openai", "account-model", original, false, EndpointResponses)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(request, "max_output_tokens").Int() != 32 || gjson.GetBytes(request, "temperature").Float() != 0.4 || gjson.GetBytes(request, "parallel_tool_calls").Bool() || gjson.GetBytes(request, "reasoning").Exists() {
		t.Fatalf("lost controls or invented defaults: %s", request)
	}
	reply := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`)
	out, err := ResponseFromEndpoint(context.Background(), EndpointResponses, "openai", "account-model", original, request, reply)
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("nonstream conversion: %s %v", out, err)
	}
	var state any
	frames, err := StreamFromEndpoint(context.Background(), EndpointResponses, "openai", "account-model", original, request, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"), &state)
	if err != nil || len(frames) != 1 || !strings.HasPrefix(string(frames[0]), "data: ") || !strings.HasSuffix(string(frames[0]), "\n\n") || !strings.Contains(string(frames[0]), "OK") {
		t.Fatalf("SSE conversion: %q %v", frames, err)
	}
}
