package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var errGatewayTimeout = errors.New("gateway timeout")

type upstreamTimeoutObserverKey struct{}

func withUpstreamTimeoutObserver(ctx context.Context, observer func()) context.Context {
	return context.WithValue(ctx, upstreamTimeoutObserverKey{}, observer)
}

type gatewayTimeoutError struct{ stage string }

func (e *gatewayTimeoutError) Error() string        { return "网关超时（非上游断流）：" + e.stage }
func (e *gatewayTimeoutError) Is(target error) bool { return target == errGatewayTimeout }
func (e *gatewayTimeoutError) Timeout() bool        { return true }

func streamIdleTimeout() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("UPSTREAM_STREAM_IDLE_TIMEOUT"))); err == nil && d > 0 {
		return d
	}
	return 180 * time.Second
}

func doUpstreamRequest(client *http.Client, req *http.Request, stream bool) (*http.Response, error) {
	if !stream {
		return client.Do(req)
	}
	return doStreamingRequest(client, req, 60*time.Second, streamIdleTimeout())
}

func doStreamingRequest(client *http.Client, req *http.Request, headerTimeout, idleTimeout time.Duration) (*http.Response, error) {
	ctx, cancelCause := context.WithCancelCause(req.Context())
	cancel := func(err error) {
		// Preserve locally-owned timeouts across asynchronous protocol bridges.
		// Notify before cancellation wakes readers, so recovery never races the signal.
		if errors.Is(err, errGatewayTimeout) && ctx.Err() == nil {
			if observe, ok := req.Context().Value(upstreamTimeoutObserverKey{}).(func()); ok && observe != nil {
				observe()
			}
		}
		cancelCause(err)
	}
	// Copy the client, not its Transport: connections/proxies remain shared and
	// concurrent non-streaming calls retain their original total timeout.
	streamClient := *client
	streamClient.Timeout = 0
	timer := time.AfterFunc(headerTimeout, func() { cancel(&gatewayTimeoutError{stage: "等待上游响应头超时"}) })
	resp, err := streamClient.Do(req.Clone(ctx))
	timer.Stop()
	if err != nil {
		cause := context.Cause(ctx)
		cancel(nil)
		if errors.Is(cause, errGatewayTimeout) {
			return nil, cause
		}
		return nil, err
	}
	resp.Body = &guardedStreamBody{body: resp.Body, ctx: ctx, cancel: cancel, idle: idleTimeout,
		sse: strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")}
	return resp, nil
}

type guardedStreamBody struct {
	body    io.ReadCloser
	ctx     context.Context
	cancel  context.CancelCauseFunc
	idle    time.Duration
	quiet   time.Duration
	sse     bool
	pending []byte
	event   string
}

func (b *guardedStreamBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, context.Cause(b.ctx)
	}
	remaining := b.idle - b.quiet
	if remaining <= 0 {
		err := &gatewayTimeoutError{stage: "等待有效流式数据超时"}
		b.cancel(err)
		return 0, err
	}
	started := time.Now()
	timer := time.AfterFunc(remaining, func() { b.cancel(&gatewayTimeoutError{stage: "等待有效流式数据超时"}) })
	n, err := b.body.Read(p)
	timer.Stop()
	// Count only time inside Read, never time blocked writing to a slow client.
	b.quiet += time.Since(started)
	if n > 0 && (!b.sse || b.hasActivity(p[:n])) {
		b.quiet = 0
	}
	if cause := context.Cause(b.ctx); errors.Is(cause, errGatewayTimeout) {
		return n, cause
	}
	return n, err
}

func (b *guardedStreamBody) Close() error {
	b.cancel(nil)
	return b.body.Close()
}

func (b *guardedStreamBody) hasActivity(p []byte) bool {
	b.pending = append(b.pending, p...)
	active := false
	for {
		i := bytes.IndexByte(b.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(b.pending[:i])
		if len(line) == 0 {
			b.event = ""
		} else if bytes.HasPrefix(line, []byte("event:")) {
			b.event = strings.ToLower(strings.TrimSpace(string(line[6:])))
		} else if bytes.HasPrefix(line, []byte("data:")) && !isStreamHeartbeat(b.event) {
			data := bytes.TrimSpace(line[5:])
			if len(data) > 0 && !isStreamHeartbeat(strings.ToLower(string(data))) {
				var value map[string]any
				if json.Unmarshal(data, &value) != nil {
					active = true // Leave malformed/non-JSON data to the protocol parser.
				} else {
					kind := strings.ToLower(firstNonEmpty(anyString(value["type"]), anyString(value["event"])))
					if len(value) > 0 && !isStreamHeartbeat(kind) {
						active = true
					}
				}
			}
		}
		b.pending = b.pending[i+1:]
	}
	// Match the existing SSE parser bound; do not retain response contents.
	if len(b.pending) > maxSSEPreludeBytes {
		b.pending = nil
	}
	return active
}

func isStreamHeartbeat(kind string) bool {
	return kind == "ping" || kind == "heartbeat" || kind == "keepalive" || kind == "keep-alive"
}

func streamErrorDetail(err error) map[string]any {
	if errors.Is(err, errGatewayTimeout) {
		return map[string]any{"code": "gateway_timeout", "type": "gateway_error", "message": err.Error()}
	}
	return map[string]any{"message": fmt.Sprintf("upstream stream interrupted: %v", err), "type": "upstream_error"}
}
