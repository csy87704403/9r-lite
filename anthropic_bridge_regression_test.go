package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicToolResultImagesBecomeRealOpenAIUserImages(t *testing.T) {
	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"screen1","name":"screenshot","input":{}},{"type":"tool_use","id":"screen2","name":"screenshot","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"screen1","content":[{"type":"text","text":"first screenshot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]},{"type":"tool_result","tool_use_id":"screen2","content":[{"type":"image","source":{"type":"url","url":"https://example.com/screen2.png"}}]},{"type":"text","text":"compare both"}]}]}`)
	converted, err := anthropicRequestToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(converted, &request); err != nil {
		t.Fatal(err)
	}
	messages := anySlice(request["messages"])
	if len(messages) != 5 {
		t.Fatalf("expected user/assistant/tool/tool/user, got %s", converted)
	}
	for index, id := range []string{"screen1", "screen2"} {
		message := anyMap(messages[index+2])
		if anyString(message["role"]) != "tool" || anyString(message["tool_call_id"]) != id || strings.Contains(anyString(message["content"]), "AA==") {
			t.Fatalf("parallel tool result lost its semantic role: %s", converted)
		}
	}
	imageMessage := anyMap(messages[4])
	if anyString(imageMessage["role"]) != "user" || !messageContentHasImage(imageMessage["content"], false) {
		t.Fatalf("images were not attached as real user image parts: %s", converted)
	}
	images := []string{}
	for _, value := range anySlice(imageMessage["content"]) {
		part := anyMap(value)
		if anyString(part["type"]) == "image_url" {
			images = append(images, anyString(anyMap(part["image_url"])["url"]))
		}
	}
	if len(images) != 2 || images[0] != "data:image/png;base64,AA==" || images[1] != "https://example.com/screen2.png" || !strings.Contains(string(converted), "first screenshot") || !strings.Contains(string(converted), "compare both") {
		t.Fatalf("image URLs or related text were lost: %s", converted)
	}
}

type anthropicAuditErrorReader struct{}

func (anthropicAuditErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("upstream read failed")
}

func TestAnthropicStreamConversionPreservesErrorsAndTruncation(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n"
	content := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	upstreamError := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"too many requests\"}}\n\n"
	for _, test := range []struct {
		name      string
		body      string
		readError bool
	}{
		{name: "error only", body: upstreamError},
		{name: "error after role", body: start + upstreamError},
		{name: "error after content", body: start + content + upstreamError},
		{name: "incomplete empty stream"},
		{name: "incomplete content", body: start + content},
		{name: "scan failure", body: start, readError: true},
		{name: "invalid event", body: start + "data: invalid-json\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(test.body)
			if test.readError {
				reader = io.MultiReader(reader, anthropicAuditErrorReader{})
			}
			rr := httptest.NewRecorder()
			anthropicStreamToOpenAI(rr, reader, "audit/model")
			out := rr.Body.String()
			if !strings.Contains(out, `"error":`) || strings.Contains(out, "[DONE]") || strings.Contains(out, `"finish_reason":"stop"`) {
				t.Fatalf("failed stream was converted into success: %s", out)
			}
			if strings.Contains(test.body, "too many requests") && !strings.Contains(out, "too many requests") {
				t.Fatalf("upstream error message was lost: %s", out)
			}
		})
	}
}

func TestOpenAIStreamConversionPreservesErrorsAndTruncation(t *testing.T) {
	start := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"
	content := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	upstreamError := "data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"too many requests\"}}\n\n"
	for _, test := range []struct {
		name      string
		body      string
		readError bool
	}{
		{name: "error only", body: upstreamError},
		{name: "error after role", body: start + upstreamError},
		{name: "error after content", body: start + content + upstreamError},
		{name: "incomplete empty stream"},
		{name: "incomplete content", body: start + content},
		{name: "scan failure", body: start, readError: true},
		{name: "invalid event", body: start + "data: invalid-json\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(test.body)
			if test.readError {
				reader = io.MultiReader(reader, anthropicAuditErrorReader{})
			}
			rr := httptest.NewRecorder()
			openAIStreamToAnthropic(rr, reader, "audit/model")
			out := rr.Body.String()
			if !strings.Contains(out, "event: error") || strings.Contains(out, "event: message_stop") || strings.Contains(out, `"stop_reason":"end_turn"`) {
				t.Fatalf("failed stream was converted into success: %s", out)
			}
			if test.name == "error only" && strings.Contains(out, "event: message_start") {
				t.Fatalf("error-only stream fabricated a message: %s", out)
			}
			if strings.Contains(test.body, "too many requests") && !strings.Contains(out, "too many requests") {
				t.Fatalf("upstream error message was lost: %s", out)
			}
		})
	}
}

func TestStreamConversionAcceptsExplicitFinishReasonWithoutDone(t *testing.T) {
	openAI := "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"
	rr := httptest.NewRecorder()
	openAIStreamToAnthropic(rr, strings.NewReader(openAI), "audit/model")
	if !strings.Contains(rr.Body.String(), "event: message_stop") || strings.Contains(rr.Body.String(), "event: error") {
		t.Fatalf("valid finish reason was rejected: %s", rr.Body.String())
	}
	anthropic := "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	rr = httptest.NewRecorder()
	anthropicStreamToOpenAI(rr, strings.NewReader(anthropic), "audit/model")
	if !strings.Contains(rr.Body.String(), "[DONE]") || strings.Contains(rr.Body.String(), `"error":`) {
		t.Fatalf("valid stop reason was rejected: %s", rr.Body.String())
	}
}

func TestConvertedInitialStreamErrorsRemainRetryableByAuto(t *testing.T) {
	for _, test := range []struct {
		name    string
		convert func(http.ResponseWriter, io.Reader, string)
		body    string
	}{
		{name: "Anthropic to OpenAI", convert: anthropicStreamToOpenAI, body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"quota exhausted\"}}\n\n"},
		{name: "OpenAI to Anthropic", convert: openAIStreamToAnthropic, body: "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\ndata: {\"error\":{\"message\":\"quota exhausted\"}}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := httptest.NewRecorder()
			gate := newAutoRetryWriter(target, false)
			test.convert(gate, strings.NewReader(test.body), "audit/model")
			gate.finishResponse()
			if gate.committed || target.Body.Len() != 0 || gate.status != http.StatusBadGateway || !strings.Contains(gate.body.String(), "quota exhausted") {
				t.Fatalf("converted initial error cannot be retried: committed=%v HTTP=%d buffered=%s output=%s", gate.committed, gate.status, gate.body.String(), target.Body.String())
			}
		})
	}
}

type anthropicAuditUnexpectedReadReader struct{ read bool }

func (r *anthropicAuditUnexpectedReadReader) Read([]byte) (int, error) {
	r.read = true
	return 0, errors.New("should not read beyond terminal event")
}

func TestStreamConvertersStopReadingAfterTerminalEvents(t *testing.T) {
	for _, test := range []struct {
		name    string
		convert func(http.ResponseWriter, io.Reader, string)
		body    string
	}{
		{name: "Anthropic normal stop", convert: anthropicStreamToOpenAI, body: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{name: "OpenAI normal stop", convert: openAIStreamToAnthropic, body: "data: [DONE]\n\n"},
		{name: "Anthropic error", convert: anthropicStreamToOpenAI, body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"quota exhausted\"}}\n\n"},
		{name: "OpenAI error", convert: openAIStreamToAnthropic, body: "data: {\"error\":{\"message\":\"quota exhausted\"}}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tail := &anthropicAuditUnexpectedReadReader{}
			test.convert(httptest.NewRecorder(), io.MultiReader(strings.NewReader(test.body), tail), "audit/model")
			if tail.read {
				t.Fatal("converter kept reading after the terminal event")
			}
		})
	}
}
