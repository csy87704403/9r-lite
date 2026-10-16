package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxSSEPreludeBytes = 1 << 20
const maxBufferedResponseBytes = 32 << 20

var errAutoStreamRejected = errors.New("upstream stream rejected")
var errAutoStreamComplete = errors.New("upstream stream complete")
var errResponseTooLarge = errors.New("upstream response exceeds gateway buffer limit")

func (w *autoRetryWriter) writeStream(body []byte) (int, error) {
	n, err := w.target.Write(body)
	if errors.Is(err, errAutoStreamComplete) {
		err = nil
	}
	w.writeErr = err
	if err != nil {
		return n, err
	}
	if len(w.stream.failure) > 0 {
		return n, errAutoStreamRejected
	}
	if w.stream.terminated {
		return n, errAutoStreamComplete
	}
	return n, nil
}

// Inspect complete events, not TCP chunks. Role-only deltas and keepalives do
// not commit a response: an initial error can still fall back safely.
type sseInspection struct {
	pending    []byte
	event      []byte
	output     bool
	complete   bool
	terminated bool
	failure    []byte
}

func (s *sseInspection) fail(message string) {
	if len(s.failure) == 0 {
		s.failure = mustJSON(map[string]any{"error": map[string]any{"message": message}})
	}
}

func (s *sseInspection) feed(p []byte) {
	s.pending = append(s.pending, p...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSuffix(s.pending[:i], []byte{'\r'})
		if len(line) == 0 {
			s.inspectEvent()
			s.event = s.event[:0]
		} else if len(s.event)+len(line)+1 <= maxSSEPreludeBytes {
			s.event = append(s.event, line...)
			s.event = append(s.event, '\n')
		} else {
			s.fail("upstream stream event exceeded limit")
		}
		s.pending = s.pending[i+1:]
	}
	if len(s.pending) > maxSSEPreludeBytes {
		s.fail("upstream stream line exceeded limit")
		s.pending = nil
	}
}

func (s *sseInspection) finish() {
	if len(s.pending) > 0 {
		s.event = append(s.event, s.pending...)
		s.pending = nil
	}
	if len(s.event) > 0 {
		s.inspectEvent()
		s.event = nil
	}
}

func (s *sseInspection) inspectEvent() {
	var data []byte
	var event string
	for _, line := range bytes.Split(s.event, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimPrefix(line[5:], []byte{' '})...)
		} else if bytes.HasPrefix(line, []byte("event:")) {
			event = strings.TrimSpace(string(line[6:]))
		}
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		s.complete = true
		s.terminated = true
		return
	}
	if len(data) == 0 {
		return
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		s.fail("upstream returned an invalid stream event")
		return
	}
	kind := firstNonEmpty(anyString(payload["type"]), event)
	if event == "error" || kind == "error" || kind == "response.failed" || probeResponseHasExplicitError(data) {
		if len(s.failure) == 0 {
			if payload["error"] == nil {
				data = mustJSON(map[string]any{"error": payload})
			}
			s.failure = append([]byte(nil), data[:min(len(data), 64<<10)]...)
		}
		return
	}
	for _, value := range anySlice(payload["choices"]) {
		choice := anyMap(value)
		delta := anyMap(choice["delta"])
		if len(delta) == 0 {
			delta = anyMap(choice["message"])
		}
		if streamDeltaHasOutput(delta) {
			s.output = true
		}
		if anyString(choice["finish_reason"]) != "" {
			s.complete = true
		}
	}
	switch kind {
	case "content_block_start":
		block := anyMap(payload["content_block"])
		if anyString(block["type"]) == "tool_use" || anyString(block["text"]) != "" {
			s.output = true
		}
	case "content_block_delta":
		delta := anyMap(payload["delta"])
		if anyString(delta["text"]) != "" || anyString(delta["thinking"]) != "" || anyString(delta["partial_json"]) != "" {
			s.output = true
		}
	case "message_delta":
		if anyString(anyMap(payload["delta"])["stop_reason"]) != "" {
			s.complete = true
		}
	case "message_stop", "response.completed":
		s.complete = true
		s.terminated = true
	}
}

func streamDeltaHasOutput(delta map[string]any) bool {
	for _, key := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
		if anyString(delta[key]) != "" {
			return true
		}
	}
	return len(anySlice(delta["tool_calls"])) > 0 || len(anyMap(delta["function_call"])) > 0 || len(anyMap(delta["audio"])) > 0
}

func (w *autoRetryWriter) finishResponse() {
	if w.bufferOK || w.status < 200 || w.status > 299 || !strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") {
		if w.status >= 200 && w.status <= 299 && (w.readErr != nil || (w.validateJSON && !json.Valid(w.body.Bytes()))) {
			w.rejectJSONResponse("upstream returned incomplete or invalid JSON")
		}
		return
	}
	w.stream.finish()
	if w.readErr != nil {
		w.stream.fail("upstream stream read failed")
	}
	if len(w.stream.failure) == 0 && (!w.stream.complete || !w.stream.output) {
		w.stream.fail("upstream stream ended without a complete model response")
	}
	if w.writeErr != nil {
		w.stream.fail("stream delivery failed")
	}
	if !w.committed && len(w.stream.failure) > 0 {
		w.header.Del("Content-Length")
		w.header.Set("Content-Type", "application/json; charset=utf-8")
		w.status = http.StatusBadGateway
		w.body.Reset()
		_, _ = w.body.Write(w.stream.failure)
	}
}

func (w *autoRetryWriter) rejectJSONResponse(message string) {
	w.status = http.StatusBadGateway
	w.header.Del("Content-Length")
	w.header.Set("Content-Type", "application/json; charset=utf-8")
	w.body.Reset()
	_, _ = w.body.Write(mustJSON(map[string]any{"error": map[string]any{"message": message}}))
}

func (s *Server) autoCandidateIDs(candidate string) (string, string) {
	id, model, _ := strings.Cut(candidate, "/")
	if p, ok := s.providerByRouteID(id); ok {
		id = p.ID
	}
	return id, model
}

func (s *Server) clearAutoModelFailureForRequest(id, model string, started time.Time) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	until := s.autoFailedModels[id][model]
	// A response started before this failure cannot establish recovery.
	if !until.IsZero() && until.Add(-autoFailureCooldown).After(started) {
		return
	}
	delete(s.autoFailedModels[id], model)
	if len(s.autoFailedModels[id]) == 0 {
		delete(s.autoFailedModels, id)
	}
}

func isAutoRequestSpecificError(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"max_tokens exceeds", "max tokens exceeds", "output token limit", "output cap",
		"context length exceeded", "maximum context length", "context window exceeded",
		"tool calling is not supported", "tool use is not supported", "tools are not supported",
		"image input is not supported", "vision is not supported", "multimodal is not supported",
		"upstream response exceeds gateway buffer limit",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (s *Server) coolDownAutoFailure(id, model string, body []byte) {
	if !isAutoRequestSpecificError(body) {
		s.markAutoModelFailed(id, model)
	}
}

func (s *Server) commitAutoRequestSuccess(key, candidate string, vision, newImage bool, ticket uint64, started time.Time) {
	if s.commitAutoAgentRequestSuccess(key, candidate, vision, newImage, ticket) {
		id, model := s.autoCandidateIDs(candidate)
		s.clearAutoModelFailureForRequest(id, model, started)
	}
}

func incompleteStreamError(err error) []byte {
	return mustJSON(map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream stream interrupted: %v", err), "type": "upstream_error"}})
}
