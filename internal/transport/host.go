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
	finish := func() {
		if !stop() {
			<-done
		}
		cancel()
	}
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
		if err == nil {
			err = errors.New("host returned no stream")
		}
		return Stream{}, err
	}
	// Register cancellation until CloseStream, including before first headers.
	c.streamsStore(out.ID, finish)
	return Stream{out.StatusCode, out.Headers, out.ID}, nil
}

// Stream bookkeeping is per Client, never shared between plugin generations.

func (c *Client) streamsStore(id string, finish func()) { c.streams.Store(id, finish) }
func (c *Client) ReadStream(ctx context.Context, id string) (StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		return StreamChunk{}, err
	}
	var out StreamChunk
	err := c.Call("host.http.stream_read", streamID{id}, &out)
	return out, err
}
func (c *Client) CloseStream(_ context.Context, id string) error {
	err := c.Call("host.http.stream_close", streamID{id}, nil)
	if f, ok := c.streams.LoadAndDelete(id); ok {
		f.(func())()
	}
	return err
}
func (c *Client) Emit(ctx context.Context, id string, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Call("host.stream.emit", struct {
		ID      string `json:"stream_id"`
		Payload []byte `json:"payload"`
	}{id, b}, nil)
}
func (c *Client) CloseOutput(_ context.Context, id, message string) {
	_ = c.Call("host.stream.close", struct {
		ID    string `json:"stream_id"`
		Error string `json:"error,omitempty"`
	}{id, message}, nil)
}
