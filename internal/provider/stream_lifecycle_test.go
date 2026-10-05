package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cliproxyapi-github-copilot/internal/transport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPumpStreamStopsAtTerminalEvent(t *testing.T) {
	for _, tc := range []struct{ name, endpoint, destination, frame string }{
		{"chat", "/chat/completions", "openai", "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"responses completed", "/responses", "openai-response", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"},
		{"responses incomplete", "/responses", "openai-response", "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n"},
		{"messages", "/v1/messages", "claude", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A valid terminal must stop within this chunk, without inspecting
			// trailing frames or waiting for upstream EOF (which may never come).
			h := &recordingStreamHost{chunks: []transport.StreamChunk{
				{Payload: []byte(tc.frame + "event: error\ndata: {\"error\":\"private-details\"}\n\ndata: unfinished")},
				{Error: "must not read past terminal"},
			}}
			s := newTestService(t, h)
			s.pumpStream(context.Background(), "out", tc.endpoint, tc.destination, "copilot/model", nil, nil, transport.Stream{ID: "in"}, streamIdleTimeout)
			if h.message != "" || h.closes != 1 || len(h.outputs) != 1 || len(h.chunks) != 1 {
				t.Fatalf("message=%q closes=%d outputs=%q remaining=%d", h.message, h.closes, h.outputs, len(h.chunks))
			}
		})
	}
}

func TestPumpStreamChatRequiresEveryChoiceFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		ok           bool
	}{
		{"done alone", "", false},
		{"missing reason", `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`, false},
		{"null reason", `{"choices":[{"index":0,"finish_reason":null}]}`, false},
		{"empty reason", `{"choices":[{"index":0,"finish_reason":""}]}`, false},
		{"blank reason", `{"choices":[{"index":0,"finish_reason":" "}]}`, false},
		{"negative choice index", `{"choices":[{"index":-1,"finish_reason":"stop"}]}`, false},
		{"one unfinished choice", `{"choices":[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":null}]}`, false},
		{"all choices finish", `{"choices":[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":"length"}]}`, true},
		{"tool finish", `{"choices":[{"index":0,"finish_reason":"tool_calls"}]}`, true},
		{"usage after finish", "{\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":7}}", true},
		{"malformed terminal JSON", `{"choices":[{"index":0,"finish_reason":"stop"}],}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := ""
			if tc.events != "" {
				frame = "data: " + tc.events + "\n\n"
			}
			frame += "data: [DONE]\n\n"
			h := &recordingStreamHost{chunks: []transport.StreamChunk{{Payload: []byte(frame)}}}
			s := newTestService(t, h)
			s.pumpStream(context.Background(), "out", "/chat/completions", "openai", "copilot/model", nil, nil, transport.Stream{ID: "in"}, streamIdleTimeout)
			if (h.message == "") != tc.ok || h.closes != 1 {
				t.Fatalf("ok=%v message=%q closes=%d", tc.ok, h.message, h.closes)
			}
		})
	}
}

func TestPumpStreamBoundsTrackedChoices(t *testing.T) {
	var frames strings.Builder
	for i := 0; i <= streamMaxChoices; i++ {
		fmt.Fprintf(&frames, "data: {\"choices\":[{\"index\":%d,\"finish_reason\":\"stop\"}]}\n\n", i)
	}
	frames.WriteString("data: [DONE]\n\n")
	h := &recordingStreamHost{chunks: []transport.StreamChunk{{Payload: []byte(frames.String())}}}
	s := newTestService(t, h)
	s.pumpStream(context.Background(), "out", "/chat/completions", "openai", "copilot/model", nil, nil, transport.Stream{ID: "in"}, streamIdleTimeout)
	if h.message == "" || h.closes != 1 || len(h.outputs) != streamMaxChoices {
		t.Fatalf("unbounded choice state: message=%q closes=%d outputs=%d", h.message, h.closes, len(h.outputs))
	}
}

func TestPumpStreamRejectsEOFEvenWithFinishReason(t *testing.T) {
	for _, tc := range []struct{ endpoint, destination, frame string }{
		{"/chat/completions", "openai", "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\n"},
		{"/responses", "openai-response", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
		{"/v1/messages", "claude", "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"},
	} {
		t.Run(tc.destination, func(t *testing.T) {
			h := &recordingStreamHost{chunks: []transport.StreamChunk{{Payload: []byte(tc.frame), Done: true}}}
			s := newTestService(t, h)
			s.pumpStream(context.Background(), "out", tc.endpoint, tc.destination, "copilot/model", nil, nil, transport.Stream{ID: "in"}, streamIdleTimeout)
			if h.message == "" || h.closes != 1 {
				t.Fatalf("premature EOF accepted: message=%q closes=%d", h.message, h.closes)
			}
		})
	}
}

// nativeLifecycleHost uses the actual transport.Client. Its synthetic ABI reads
// ignore Go contexts and unblock only when the host close/cancel callback runs.
// This catches timers that merely return from an abandoned reader goroutine.
type nativeLifecycleHost struct {
	*transport.Client
	blockHeaders bool
	status       int
	firstPayload []byte
	continuous   bool
	blockEmit    bool
	canceled     chan struct{}
	readStarted  chan struct{}
	emitStarted  chan struct{}
	outputDone   chan string
	outputClosed chan struct{}
	cancelOnce   sync.Once
	readOnce     sync.Once
	emitOnce     sync.Once
	outputOnce   sync.Once
	opens        atomic.Int64
	reads        atomic.Int64
	emits        atomic.Int64
	closes       atomic.Int64
}

func newNativeLifecycleHost(t *testing.T) *nativeLifecycleHost {
	t.Helper()
	h := &nativeLifecycleHost{status: 200, canceled: make(chan struct{}), readStarted: make(chan struct{}), emitStarted: make(chan struct{}), outputDone: make(chan string, 1), outputClosed: make(chan struct{})}
	h.Client = transport.New(h)
	return h
}

func (h *nativeLifecycleHost) cancel() { h.cancelOnce.Do(func() { close(h.canceled) }) }
func (h *nativeLifecycleHost) Do(_ context.Context, _ string, req transport.Request) (transport.Response, error) {
	if strings.Contains(req.URL, "/copilot_internal/") {
		return tokenReply(time.Now(), "synthetic-copilot-token"), nil
	}
	return reply(`{"data":[{"id":"model","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]}]}`), nil
}
func (h *nativeLifecycleHost) Call(method string, in, out any) error {
	assign := func(value any) error {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	switch method {
	case "host.http.operation_open":
		return assign(map[string]string{"operation_id": "synthetic-operation"})
	case "host.http.do_stream":
		h.opens.Add(1)
		if h.blockHeaders {
			<-h.canceled
			return errors.New("synthetic canceled headers")
		}
		return assign(map[string]any{"status_code": h.status, "stream_id": "synthetic-upstream", "headers": http.Header{}})
	case "host.http.cancel":
		h.cancel()
		return nil
	case "host.http.stream_close":
		h.closes.Add(1)
		h.cancel()
		return nil
	case "host.http.stream_read":
		read := h.reads.Add(1)
		h.readOnce.Do(func() { close(h.readStarted) })
		if read == 1 && len(h.firstPayload) > 0 {
			return assign(transport.StreamChunk{Payload: h.firstPayload})
		}
		if h.continuous {
			select {
			case <-time.After(5 * time.Millisecond):
				return assign(transport.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")})
			case <-h.canceled:
			}
		} else {
			<-h.canceled
		}
		return errors.New("synthetic canceled read")
	case "host.stream.emit":
		h.emits.Add(1)
		h.emitOnce.Do(func() { close(h.emitStarted) })
		if h.blockEmit {
			<-h.outputClosed
			return errors.New("synthetic canceled output")
		}
		return nil
	case "host.stream.close":
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var closeRequest struct{ Error string }
		if err := json.Unmarshal(b, &closeRequest); err != nil {
			return err
		}
		h.outputOnce.Do(func() { h.outputDone <- closeRequest.Error; close(h.outputClosed) })
		return nil
	default:
		return errors.New("unexpected synthetic callback: " + method)
	}
}

func lifecycleRequest() ExecuteRequest {
	return ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "synthetic-account", Model: "copilot/model", SourceFormat: "openai", StorageJSON: []byte(`{"type":"copilot","github_access_token":"synthetic-access-token"}`), Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, StreamID: "synthetic-output"}
}

func awaitLifecycle(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestStreamTimeoutBeforeHeadersAndErrorBody(t *testing.T) {
	for _, tc := range []struct {
		name         string
		blockHeaders bool
		limits       streamTimeouts
	}{
		{"idle before headers", true, streamTimeouts{idle: 25 * time.Millisecond, total: time.Second}},
		{"total before headers", true, streamTimeouts{idle: time.Second, total: 25 * time.Millisecond}},
		{"idle error body", false, streamTimeouts{idle: 25 * time.Millisecond, total: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newNativeLifecycleHost(t)
			h.blockHeaders, h.status = tc.blockHeaders, http.StatusServiceUnavailable
			s := newTestService(t, h)
			t.Cleanup(h.cancel)
			done := make(chan error, 1)
			go func() {
				_, err := s.executeStreamWithTimeouts(context.Background(), lifecycleRequest(), tc.limits)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled upstream succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timeout did not unblock the native callback")
			}
			awaitLifecycle(t, h.canceled, "upstream cancellation")
			if h.opens.Load() != 1 || h.emits.Load() != 0 {
				t.Fatalf("unexpected replay or output: opens=%d emits=%d", h.opens.Load(), h.emits.Load())
			}
		})
	}
}

func TestStreamTimeoutAfterHeaders(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		first, continuous, emit bool
		limits                  streamTimeouts
	}{
		{"idle before first event", false, false, false, streamTimeouts{idle: 25 * time.Millisecond, total: time.Second}},
		{"idle after partial output", true, false, false, streamTimeouts{idle: 25 * time.Millisecond, total: time.Second}},
		{"total during active output", false, true, false, streamTimeouts{idle: time.Second, total: 40 * time.Millisecond}},
		{"total during blocked emit", true, false, true, streamTimeouts{idle: time.Second, total: 40 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newNativeLifecycleHost(t)
			h.continuous, h.blockEmit = tc.continuous, tc.emit
			if tc.first {
				h.firstPayload = []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			}
			s := newTestService(t, h)
			t.Cleanup(func() { h.cancel(); h.CloseOutput(context.Background(), "synthetic-output", "test cleanup") })
			if _, err := s.executeStreamWithTimeouts(context.Background(), lifecycleRequest(), tc.limits); err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-h.outputDone:
				if message == "" || strings.Contains(message, "synthetic") {
					t.Fatalf("missing or unsafe failure: %q", message)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timeout left stream blocked")
			}
			s.Shutdown() // also joins pump cleanup and native cancellation callbacks
			awaitLifecycle(t, h.canceled, "upstream cancellation")
			if h.opens.Load() != 1 || (tc.first || tc.continuous) && h.emits.Load() == 0 {
				t.Fatalf("unexpected replay or missing output: opens=%d emits=%d", h.opens.Load(), h.emits.Load())
			}
		})
	}
}

func TestStreamPreservesCallerCancellationAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "shutdown"}[shutdown], func(t *testing.T) {
			h := newNativeLifecycleHost(t)
			s := newTestService(t, h)
			t.Cleanup(h.cancel)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := s.executeStreamWithTimeouts(ctx, lifecycleRequest(), streamTimeouts{idle: time.Second, total: time.Second}); err != nil {
				t.Fatal(err)
			}
			awaitLifecycle(t, h.readStarted, "blocked native read")
			if shutdown {
				done := make(chan struct{})
				go func() { s.Shutdown(); close(done) }()
				awaitLifecycle(t, done, "service shutdown")
			} else {
				cancel()
			}
			awaitLifecycle(t, h.outputClosed, "output closure")
			s.Shutdown()
			awaitLifecycle(t, h.canceled, "upstream cancellation")
		})
	}
}
