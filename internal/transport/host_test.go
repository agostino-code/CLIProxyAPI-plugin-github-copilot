package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
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
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream headers blocked cancellation")
	}
}

func TestReadDeadlineCancelsNativeStreamAndWaitsForCleanup(t *testing.T) {
	readStarted := make(chan struct{})
	unblockRead := make(chan struct{})
	readUnblocked := make(chan struct{})
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var unblockOnce, releaseOnce sync.Once
	var closes, cancels atomic.Int32
	t.Cleanup(func() {
		unblockOnce.Do(func() { close(unblockRead) })
		releaseOnce.Do(func() { close(releaseCleanup) })
	})
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_read":
			close(readStarted)
			<-unblockRead
			close(readUnblocked)
			return errors.New("native callback cancellation details")
		case "host.http.stream_close":
			closes.Add(1)
			unblockOnce.Do(func() { close(unblockRead) })
			return nil
		case "host.http.cancel":
			cancels.Add(1)
			close(cleanupStarted)
			<-releaseCleanup
			return nil
		}
		return errors.New("unexpected callback")
	}))
	stream, err := client.OpenStream(context.Background(), "request", Request{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.ReadStream(ctx, stream.ID); done <- err }()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("native read did not start")
	}
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("read deadline did not cancel the host operation")
	}
	select {
	case <-readUnblocked:
	case <-time.After(time.Second):
		t.Fatal("host cancellation did not unblock the native read")
	}
	select {
	case err := <-done:
		t.Fatalf("read returned before its cancellation callback finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseCleanup) })
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("read error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native read remained blocked after cancellation cleanup")
	}
	if err := client.CloseStream(context.Background(), stream.ID); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("cleanup callbacks: closes=%d cancels=%d", closes.Load(), cancels.Load())
	}
	if _, ok := client.streams.Load(stream.ID); ok {
		t.Fatal("canceled stream bookkeeping was retained")
	}
}

func TestCanceledReadClosesStreamBeforeReturning(t *testing.T) {
	var closes, cancels, reads int
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_read":
			reads++
			return nil
		case "host.http.stream_close":
			closes++
			return nil
		case "host.http.cancel":
			cancels++
			return nil
		}
		return errors.New("unexpected callback")
	}))
	stream, err := client.OpenStream(context.Background(), "request", Request{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ReadStream(ctx, stream.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v, want context.Canceled", err)
	}
	if reads != 0 || closes != 1 || cancels != 1 {
		t.Fatalf("canceled read callbacks: reads=%d closes=%d cancels=%d", reads, closes, cancels)
	}
}

func TestSuccessfulReadPreservesStreamLifetime(t *testing.T) {
	var closes, cancels atomic.Int32
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_read":
			return assign(out, StreamChunk{Payload: []byte("chunk")})
		case "host.http.stream_close":
			closes.Add(1)
			return nil
		case "host.http.cancel":
			cancels.Add(1)
			return nil
		}
		return errors.New("unexpected callback")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.OpenStream(ctx, "request", Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseStream(context.Background(), stream.ID)
	for i := 0; i < 2; i++ {
		readCtx, readCancel := context.WithCancel(ctx)
		chunk, err := client.ReadStream(readCtx, stream.ID)
		readCancel()
		if err != nil || string(chunk.Payload) != "chunk" {
			t.Fatalf("read %d: chunk=%+v err=%v", i, chunk, err)
		}
	}
	if closes.Load() != 0 || cancels.Load() != 0 {
		t.Fatalf("successful open/read canceled stream: closes=%d cancels=%d", closes.Load(), cancels.Load())
	}
	if err := client.CloseStream(context.Background(), stream.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	if closes.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("cleanup callbacks: closes=%d cancels=%d", closes.Load(), cancels.Load())
	}
}

func TestConcurrentCloseWaitsForCleanup(t *testing.T) {
	closeStarted, releaseClose := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseClose) }) })
	var closes, cancels atomic.Int32
	closeErr := errors.New("close failed")
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_close":
			closes.Add(1)
			close(closeStarted)
			<-releaseClose
			return closeErr
		case "host.http.cancel":
			cancels.Add(1)
			return nil
		}
		return errors.New("unexpected callback")
	}))
	stream, err := client.OpenStream(context.Background(), "request", Request{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 8)
	go func() { done <- client.CloseStream(context.Background(), stream.ID) }()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("close did not start")
	}
	for i := 0; i < 7; i++ {
		go func() { done <- client.CloseStream(context.Background(), stream.ID) }()
	}
	select {
	case err := <-done:
		t.Fatalf("concurrent close returned before cleanup finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseClose) })
	for i := 0; i < 8; i++ {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, closeErr) {
				t.Fatalf("unexpected close error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent close blocked after cleanup finished")
		}
	}
	if closes.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("cleanup callbacks: closes=%d cancels=%d", closes.Load(), cancels.Load())
	}
}

func TestCancellationRacingWithSuccessfulHeaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var closes, cancels atomic.Int32
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.http.operation_open":
			return assign(out, map[string]string{"operation_id": "op"})
		case "host.http.do_stream":
			cancel()
			return assign(out, map[string]any{"status_code": 200, "stream_id": "stream"})
		case "host.http.stream_close":
			closes.Add(1)
			return nil
		case "host.http.cancel":
			cancels.Add(1)
			return nil
		}
		return errors.New("unexpected callback")
	}))
	stream, err := client.OpenStream(ctx, "request", Request{})
	if !errors.Is(err, context.Canceled) || stream.ID != "" {
		t.Fatalf("canceled open returned stream=%+v err=%v", stream, err)
	}
	if closes.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("cleanup callbacks: closes=%d cancels=%d", closes.Load(), cancels.Load())
	}
	if _, ok := client.streams.Load("stream"); ok {
		t.Fatal("canceled open retained stream bookkeeping")
	}
}

func TestEmitDeadlineClosesOutputAndWaitsForCleanup(t *testing.T) {
	emitStarted, outputClosed := make(chan struct{}), make(chan struct{})
	emitUnblocked, releaseClose := make(chan struct{}), make(chan struct{})
	var unblockOnce, releaseOnce sync.Once
	t.Cleanup(func() {
		unblockOnce.Do(func() { close(outputClosed) })
		releaseOnce.Do(func() { close(releaseClose) })
	})
	var closes atomic.Int32
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.stream.emit":
			close(emitStarted)
			<-outputClosed
			close(emitUnblocked)
			return errors.New("native callback cancellation details")
		case "host.stream.close":
			var request struct {
				ID    string `json:"stream_id"`
				Error string `json:"error"`
			}
			if err := assign(&request, in); err != nil {
				return err
			}
			if request.ID != "output" || request.Error != "response stream canceled" {
				t.Errorf("unexpected close request: %+v", request)
			}
			closes.Add(1)
			unblockOnce.Do(func() { close(outputClosed) })
			<-releaseClose
			return nil
		}
		return errors.New("unexpected callback")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Emit(ctx, "output", []byte("chunk")) }()
	select {
	case <-emitStarted:
	case <-time.After(time.Second):
		t.Fatal("native emit did not start")
	}
	select {
	case <-emitUnblocked:
	case <-time.After(time.Second):
		t.Fatal("deadline did not close output and unblock native emit")
	}
	select {
	case err := <-done:
		t.Fatalf("emit returned before its cancellation callback finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseClose) })
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("emit error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit remained blocked after cancellation cleanup")
	}
	if closes.Load() != 1 {
		t.Fatalf("output close callbacks = %d, want 1", closes.Load())
	}
}

func TestSuccessfulEmitDetachesCancellation(t *testing.T) {
	var closes atomic.Int32
	client := New(callFunc(func(method string, in, out any) error {
		switch method {
		case "host.stream.emit":
			return nil
		case "host.stream.close":
			closes.Add(1)
			return nil
		}
		return errors.New("unexpected callback")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := client.Emit(ctx, "output", []byte("chunk")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if closes.Load() != 0 {
		t.Fatal("completed emit retained its cancellation callback")
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
