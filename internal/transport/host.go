package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Caller is the native ABI callback. It must be safe for concurrent calls.
type Caller interface{ Call(string, any, any) error }

type Client struct {
	Caller
	streams sync.Map
}

type streamState struct {
	finish    func()
	closeOnce sync.Once
	closeErr  error
}

func New(c Caller) *Client { return &Client{Caller: c} }

type operation struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
}
type request struct {
	operation
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body,omitempty"`
}
type streamID struct {
	ID string `json:"stream_id"`
}

func (c *Client) start(ctx context.Context, callback string) (operation, func(), error) {
	if err := ctx.Err(); err != nil {
		return operation{}, nil, err
	}
	op := operation{HostCallbackID: callback}
	if err := c.Call("host.http.operation_open", op, &op); err != nil {
		return op, nil, err
	}
	if op.OperationID == "" {
		return op, nil, errors.New("host returned no operation ID")
	}
	var once sync.Once
	cancel := func() { once.Do(func() { _ = c.Call("host.http.cancel", op, nil) }) }
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); cancel() })
	finish := sync.OnceFunc(func() {
		if !stop() {
			<-done
		}
		cancel()
	})
	return op, finish, nil
}

// Do consumes the host streaming transport to bound response memory before it
// crosses the native ABI; host.http.do itself has an unbounded io.ReadAll.
func (c *Client) Do(ctx context.Context, callback string, r Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stream, err := c.OpenStream(ctx, callback, r)
	if err != nil {
		return Response{}, err
	}
	defer c.CloseStream(context.Background(), stream.ID)
	out := Response{StatusCode: stream.StatusCode, Headers: stream.Headers}
	for {
		chunk, err := c.ReadStream(ctx, stream.ID)
		if err != nil {
			return Response{}, err
		}
		if chunk.Error != "" {
			return Response{}, errors.New("upstream response transport failed")
		}
		if len(out.Body)+len(chunk.Payload) > 16<<20 {
			return Response{}, errors.New("upstream response exceeds 16 MiB")
		}
		out.Body = append(out.Body, chunk.Payload...)
		if chunk.Done {
			return out, nil
		}
	}
}
func (c *Client) OpenStream(ctx context.Context, callback string, r Request) (Stream, error) {
	op, finish, err := c.start(ctx, callback)
	if err != nil {
		return Stream{}, err
	}
	var out struct {
		StatusCode int         `json:"status_code"`
		Headers    http.Header `json:"headers"`
		ID         string      `json:"stream_id"`
	}
	err = c.Call("host.http.do_stream", request{op, r.Method, r.URL, r.Headers, r.Body}, &out)
	// The host transfers the request context into the stream. Do not cancel a
	// successful operation here: stream_close owns its lifetime from this point.
	if err != nil || out.ID == "" {
		finish()
		if ctx.Err() != nil {
			return Stream{}, ctx.Err()
		}
		if err == nil {
			err = errors.New("host returned no stream")
		}
		return Stream{}, err
	}
	// Keep the operation's cancellation hook until CloseStream. Cancellation
	// may race with successful headers, so register cleanup before checking it.
	c.streams.Store(out.ID, &streamState{finish: finish})
	if err := ctx.Err(); err != nil {
		_ = c.CloseStream(context.Background(), out.ID)
		return Stream{}, err
	}
	return Stream{out.StatusCode, out.Headers, out.ID}, nil
}

// Stream bookkeeping is per Client, never shared between plugin generations.

func (c *Client) ReadStream(ctx context.Context, id string) (StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		_ = c.CloseStream(context.Background(), id)
		return StreamChunk{}, err
	}
	// Native calls cannot receive this read's context. Close the host stream on
	// cancellation to cancel its HTTP operation and unblock the synchronous
	// read; never leave a blocked native call behind in a detached goroutine.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = c.CloseStream(context.Background(), id)
	})
	var out StreamChunk
	err := c.Call("host.http.stream_read", streamID{id}, &out)
	if !stop() {
		<-done
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Also cover cancellation racing with a successful stop of the hook.
		_ = c.CloseStream(context.Background(), id)
		return StreamChunk{}, ctxErr
	}
	return out, err
}
func (c *Client) CloseStream(_ context.Context, id string) error {
	v, ok := c.streams.Load(id)
	if !ok {
		return nil
	}
	state := v.(*streamState)
	state.closeOnce.Do(func() {
		state.closeErr = c.Call("host.http.stream_close", streamID{id}, nil)
		state.finish()
	})
	// Leave the state visible until cleanup is complete so concurrent closes
	// wait for the same callbacks instead of returning while cleanup runs.
	c.streams.CompareAndDelete(id, state)
	return state.closeErr
}
func (c *Client) Emit(ctx context.Context, id string, b []byte) error {
	if err := ctx.Err(); err != nil {
		c.CloseOutput(context.Background(), id, "response stream canceled")
		return err
	}
	// A full host output queue can block the native emit call. Closing that
	// output releases its emitter even when the downstream reader is stalled.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		c.CloseOutput(context.Background(), id, "response stream canceled")
	})
	err := c.Call("host.stream.emit", struct {
		ID      string `json:"stream_id"`
		Payload []byte `json:"payload"`
	}{id, b}, nil)
	stopped := stop()
	if !stopped {
		<-done
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if stopped {
			c.CloseOutput(context.Background(), id, "response stream canceled")
		}
		return ctxErr
	}
	return err
}
func (c *Client) CloseOutput(_ context.Context, id, message string) {
	_ = c.Call("host.stream.close", struct {
		ID    string `json:"stream_id"`
		Error string `json:"error,omitempty"`
	}{id, message}, nil)
}
