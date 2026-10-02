package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type callFunc func(string, any, any) error

func (f callFunc) Call(m string, in, out any) error { return f(m, in, out) }
func assign(out, in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func TestCancellationBeforeHeaders(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			close(started)
			<-canceled
			return errors.New("canceled")
		case "host.http.cancel":
			once.Do(func() { close(canceled) })
			return nil
		}
		return errors.New("unexpected callback")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.Do(ctx, "request", Request{URL: "https://example.invalid"}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("upstream headers blocked cancellation")
	}
}
func TestBoundedBodyAndStreamCleanup(t *testing.T) {
	var closes, cancels int
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_read":
			return assign(out, StreamChunk{Payload: make([]byte, 1<<20)})
		case "host.http.stream_close":
			closes++
			return nil
		case "host.http.cancel":
			cancels++
			return nil
		}
		return errors.New("unexpected callback")
	}))
	if _, err := client.Do(context.Background(), "", Request{}); err == nil {
		t.Fatal("unbounded body accepted")
	}
	if closes != 1 || cancels != 1 {
		t.Fatalf("leaked stream: closes=%d cancels=%d", closes, cancels)
	}
}
