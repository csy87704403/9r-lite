package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	anthropicAPIVersion      = "2023-06-01"
	claudeCodeSystemPrompt   = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeCodeBillingHeader  = "x-anthropic-billing-header: cc_version=2.1.159.930; cc_entrypoint=sdk-cli; cch=3f3f2;"
	claudeCodeBetaFeatures   = "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24"
	claudeCodeCompatibleMode = "claude-code"
	anthropicRequestModeKey  = "anthropicRequestMode"
)

type anthropicRequest struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Raw    json.RawMessage `json:"-"`
}

func (s *Server) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 20<<20))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(anyString(body["model"]))
	scope, _ := s.accessScopeForRequest(r)
	if model == "auto" {
		if !scopeAllowsModel(scope, "auto") {
			writeAnthropicError(w, http.StatusForbidden, "model is not allowed by this access key: auto")
			return
		}
	} else if providerID, upstreamModel, ok := strings.Cut(model, "/"); ok {
		p, found := s.providerByRouteID(providerID)
		if !found || !p.Enabled || !scopeAllowsProviderModel(scope, p, upstreamModel) {
			writeAnthropicError(w, http.StatusForbidden, "model is not allowed by this access key: "+model)
			return
		}
	} else {
		writeAnthropicError(w, http.StatusBadRequest, "model must be provider/model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": estimateAnthropicInputTokens(body)})
}

func (s *Server) handleAnthropicModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	scope, _ := s.accessScopeForRequest(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var models []map[string]any
	for _, p := range s.enabledProviders() {
		for _, model := range s.chatModelsForProvider(ctx, p) {
			if !scopeAllowsProviderModel(scope, p, model) {
				continue
			}
			id := providerModelRef(p, model)
			models = append(models, map[string]any{
				"id":           id,
				"type":         "model",
				"display_name": id,
				"created_at":   "1970-01-01T00:00:00Z",
			})
		}
	}
	if _, ok := s.resolveAutoModel(ctx); ok && scopeAllowsModel(scope, "auto") {
		models = append(models, map[string]any{
			"id": "auto", "type": "model", "display_name": "auto", "created_at": "1970-01-01T00:00:00Z",
		})
	}
	firstID, lastID := "", ""
	if len(models) > 0 {
		firstID, _ = models[0]["id"].(string)
		lastID, _ = models[len(models)-1]["id"].(string)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": models, "has_more": false, "first_id": firstID, "last_id": lastID,
	})
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAccessKey(w, r) {
		return
	}
	scope, _ := s.accessScopeForRequest(r)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 20<<20))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Raw = raw
	requestedModel := strings.TrimSpace(req.Model)
	if requestedModel == "" {
		writeAnthropicError(w, http.StatusBadRequest, "model is required")
		return
	}
	if requestedModel == "auto" {
		if !scopeAllowsModel(scope, "auto") {
			writeAnthropicError(w, http.StatusForbidden, "model is not allowed by this access key: auto")
			return
		}
		hasImage := anthropicMessagesHaveImage(raw)
		target, ok := s.resolveAutoModel(r.Context())
		if hasImage {
			target, ok = s.resolveAutoVisionModel(r.Context())
		}
		if !ok {
			message := "auto model has no available target"
			if hasImage {
				message = "auto model has no available multimodal target"
			}
			writeAnthropicError(w, http.StatusServiceUnavailable, message)
			return
		}
		req.Model = target
		req.Raw, err = replaceModel(raw, target)
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	providerID, upstreamModel, ok := strings.Cut(req.Model, "/")
	if !ok || providerID == "" || upstreamModel == "" {
		writeAnthropicError(w, http.StatusBadRequest, "model must be provider/model")
		return
	}
	p, ok := s.providerByRouteID(providerID)
	if !ok || !p.Enabled {
		writeAnthropicError(w, http.StatusNotFound, "provider is not enabled: "+providerID)
		return
	}
	if requestedModel != "auto" && !scopeAllowsProviderModel(scope, p, upstreamModel) {
		writeAnthropicError(w, http.StatusForbidden, "model is not allowed by this access key: "+requestedModel)
		return
	}
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); !bypass && !sliceSet(s.chatModelsForProvider(r.Context(), p))[upstreamModel] {
		writeAnthropicError(w, http.StatusNotFound, "model is not published or not available: "+req.Model)
		return
	}

	if p.Type == "anthropic" {
		s.proxyAnthropicNative(w, r, p, req, upstreamModel)
		return
	}
	s.proxyOpenAIAsAnthropic(w, r, req, requestedModel)
}

func (s *Server) proxyOpenAIAsAnthropic(w http.ResponseWriter, r *http.Request, req anthropicRequest, responseModel string) {
	openAI, err := anthropicRequestToOpenAI(req.Raw)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	internal := r.Clone(r.Context())
	internal = internal.WithContext(context.WithValue(internal.Context(), internalBypassKey{}, true))
	internal.URL.Path = "/v1/chat/completions"
	internal.Body = io.NopCloser(bytes.NewReader(openAI))
	internal.ContentLength = int64(len(openAI))
	internal.Header = r.Header.Clone()
	internal.Header.Set("Content-Type", "application/json")

	if !req.Stream {
		rr := httptest.NewRecorder()
		s.handleChatCompletions(rr, internal)
		if rr.Code < 200 || rr.Code > 299 {
			writeOpenAIErrorAsAnthropic(w, rr.Code, rr.Body.Bytes())
			return
		}
		body, err := openAIResponseToAnthropic(rr.Body.Bytes(), responseModel)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(rr.Code)
		_, _ = w.Write(body)
		return
	}

	pr, pw := io.Pipe()
	bridge := newPipeResponseWriter(pw)
	go func() {
		s.handleChatCompletions(bridge, internal)
		bridge.finish()
	}()
	<-bridge.ready
	status := bridge.statusCode()
	if status < 200 || status > 299 {
		body, _ := io.ReadAll(io.LimitReader(pr, 4<<20))
		writeOpenAIErrorAsAnthropic(w, status, body)
		return
	}
	if !strings.Contains(strings.ToLower(bridge.Header().Get("Content-Type")), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(pr, 32<<20))
		converted, err := openAIResponseToAnthropic(body, responseModel)
		if err != nil {
			writeAnthropicError(w, http.StatusBadGateway, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(converted)
		return
	}
	openAIStreamToAnthropic(w, pr, responseModel)
}

func (s *Server) proxyAnthropicNative(w http.ResponseWriter, r *http.Request, p ProviderConfig, req anthropicRequest, upstreamModel string) {
	body, err := replaceModel(req.Raw, upstreamModel)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.doAnthropicRequest(r.Context(), r, p, body, upstreamModel, req.Stream)
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = copyFlush(w, resp.Body)
}

func (s *Server) proxyAnthropicAsOpenAI(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	body, err := openAIRequestToAnthropicForProvider(req.Raw, upstreamModel, p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp, err := s.doAnthropicRequest(r.Context(), r, p, body, upstreamModel, req.Stream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		writeAnthropicErrorAsOpenAI(w, resp.StatusCode, raw)
		return
	}
	model := providerModelRef(p, upstreamModel)
	if req.Stream {
		anthropicStreamToOpenAI(w, resp.Body, model)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	converted, err := anthropicResponseToOpenAI(raw, model)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(converted)
}

func (s *Server) doAnthropicRequest(ctx context.Context, incoming *http.Request, p ProviderConfig, body []byte, model string, stream bool) (*http.Response, error) {
	keys := providerAPIKeys(p)
	if len(keys) == 0 {
		return nil, errors.New("provider api_key is empty")
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return nil, errors.New("provider base_url is empty")
	}
	order := rotatingKeyOrder(p, len(keys), model)
	var lastResp *http.Response
	var lastBody []byte
	for _, keyIndex := range order {
		upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicRequestTarget(p, "/messages"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		applyAnthropicUpstreamHeaders(upReq.Header, incoming, p, keys[keyIndex], stream)
		resp, err := s.client.Do(upReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			s.markProviderKeyActive(p.ID, keyIndex, model)
			return resp, nil
		}
		lastBody, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(lastBody))
		lastResp = resp
		if isCredentialKeyError(resp.StatusCode, lastBody) {
			s.markProviderKeyFailed(p.ID, keyIndex, "", true)
			continue
		}
		if isQuotaKeyError(resp.StatusCode, lastBody) {
			s.markProviderKeyFailed(p.ID, keyIndex, model, false)
			continue
		}
		return resp, nil
	}
	if lastResp != nil {
		lastResp.Body = io.NopCloser(bytes.NewReader(lastBody))
		return lastResp, nil
	}
	return nil, errors.New("no Anthropic API key is available")
}

func openAIRequestToAnthropic(raw []byte, model string) ([]byte, error) {
	return openAIRequestToAnthropicWithMode(raw, model, false)
}

func openAIRequestToAnthropicForProvider(raw []byte, model string, p ProviderConfig) ([]byte, error) {
	return openAIRequestToAnthropicWithMode(raw, model, isClaudeCodeCompatibleProvider(p))
}

func openAIRequestToAnthropicWithMode(raw []byte, model string, claudeCodeMode bool) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":      model,
		"max_tokens": firstPositiveNumber(in["max_tokens"], in["max_completion_tokens"], 4096),
	}
	copyIfPresent(out, in, "stream", "temperature", "top_p", "top_k", "metadata")
	if stop, ok := in["stop"]; ok {
		if s, ok := stop.(string); ok {
			out["stop_sequences"] = []string{s}
		} else {
			out["stop_sequences"] = stop
		}
	}
	var system []any
	var messages []any
	for _, item := range anySlice(in["messages"]) {
		msg := anyMap(item)
		role := anyString(msg["role"])
		if role == "system" || role == "developer" {
			system = append(system, openAIContentToAnthropic(msg["content"])...)
			continue
		}
		if role == "tool" {
			content := []any{map[string]any{
				"type": "tool_result", "tool_use_id": anyString(msg["tool_call_id"]), "content": contentAsText(msg["content"]),
			}}
			messages = appendAnthropicMessage(messages, "user", content)
			continue
		}
		content := openAIContentToAnthropic(msg["content"])
		if role == "assistant" {
			for _, callValue := range anySlice(msg["tool_calls"]) {
				call := anyMap(callValue)
				fn := anyMap(call["function"])
				var input any = map[string]any{}
				args := anyString(fn["arguments"])
				if args != "" {
					if err := json.Unmarshal([]byte(args), &input); err != nil {
						input = map[string]any{"value": args}
					}
				}
				content = append(content, map[string]any{"type": "tool_use", "id": anyString(call["id"]), "name": anyString(fn["name"]), "input": input})
			}
		}
		if role != "assistant" {
			role = "user"
		}
		messages = appendAnthropicMessage(messages, role, content)
	}
	if claudeCodeMode {
		prefix := []any{}
		if !anthropicBlocksContainText(system, "x-anthropic-billing-header:") {
			prefix = append(prefix, map[string]any{"type": "text", "text": claudeCodeBillingHeader})
		}
		if !anthropicBlocksContainText(system, claudeCodeSystemPrompt) {
			prefix = append(prefix, map[string]any{"type": "text", "text": claudeCodeSystemPrompt})
		}
		system = append(prefix, system...)
	}
	if len(system) > 0 {
		out["system"] = system
	}
	out["messages"] = messages
	if tools := openAIToolsToAnthropic(in["tools"]); len(tools) > 0 {
		out["tools"] = tools
	}
	if choice := openAIToolChoiceToAnthropic(in["tool_choice"]); choice != nil {
		out["tool_choice"] = choice
	}
	return json.Marshal(out)
}

func anthropicRequestToOpenAI(raw []byte) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := map[string]any{"model": in["model"]}
	copyIfPresent(out, in, "stream", "temperature", "top_p", "metadata")
	if max := in["max_tokens"]; max != nil {
		out["max_tokens"] = max
	}
	if stop := in["stop_sequences"]; stop != nil {
		out["stop"] = stop
	}
	var messages []any
	if system := anthropicContentToOpenAI(in["system"]); len(system) > 0 {
		messages = append(messages, map[string]any{"role": "system", "content": openAIContentValue(system)})
	}
	for _, item := range anySlice(in["messages"]) {
		msg := anyMap(item)
		role := anyString(msg["role"])
		blocks := anthropicContentToOpenAI(msg["content"])
		if role == "assistant" {
			var textParts []any
			var calls []any
			for _, blockValue := range blocks {
				block := anyMap(blockValue)
				if anyString(block["type"]) == "tool_use" {
					args, _ := json.Marshal(block["input"])
					calls = append(calls, map[string]any{"id": block["id"], "type": "function", "function": map[string]any{"name": block["name"], "arguments": string(args)}})
					continue
				}
				textParts = append(textParts, block)
			}
			converted := map[string]any{"role": "assistant", "content": openAIContentValue(textParts)}
			if len(calls) > 0 {
				converted["tool_calls"] = calls
			}
			messages = append(messages, converted)
			continue
		}
		var userParts []any
		for _, blockValue := range blocks {
			block := anyMap(blockValue)
			if anyString(block["type"]) == "tool_result" {
				if len(userParts) > 0 {
					messages = append(messages, map[string]any{"role": "user", "content": openAIContentValue(userParts)})
					userParts = nil
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": block["tool_use_id"], "content": contentAsText(block["content"])})
				continue
			}
			userParts = append(userParts, block)
		}
		if len(userParts) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": openAIContentValue(userParts)})
		}
	}
	out["messages"] = messages
	if tools := anthropicToolsToOpenAI(in["tools"]); len(tools) > 0 {
		out["tools"] = tools
	}
	if choice := anthropicToolChoiceToOpenAI(in["tool_choice"]); choice != nil {
		out["tool_choice"] = choice
	}
	if stream, _ := in["stream"].(bool); stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(out)
}

func anthropicResponseToOpenAI(raw []byte, model string) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var text strings.Builder
	var calls []any
	for _, value := range anySlice(in["content"]) {
		block := anyMap(value)
		switch anyString(block["type"]) {
		case "text":
			text.WriteString(anyString(block["text"]))
		case "tool_use":
			args, _ := json.Marshal(block["input"])
			calls = append(calls, map[string]any{"id": block["id"], "type": "function", "function": map[string]any{"name": block["name"], "arguments": string(args)}})
		}
	}
	message := map[string]any{"role": "assistant", "content": text.String()}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	usage := anyMap(in["usage"])
	out := map[string]any{
		"id": firstNonEmpty(anyString(in["id"]), "chatcmpl_"+newSessionID()), "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": anthropicStopToOpenAI(anyString(in["stop_reason"]))}},
		"usage":   map[string]any{"prompt_tokens": numberValue(usage["input_tokens"]), "completion_tokens": numberValue(usage["output_tokens"]), "total_tokens": numberValue(usage["input_tokens"]) + numberValue(usage["output_tokens"])},
	}
	return json.Marshal(out)
}

func openAIResponseToAnthropic(raw []byte, model string) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	choices := anySlice(in["choices"])
	if len(choices) == 0 {
		return nil, errors.New("OpenAI response has no choices")
	}
	choice := anyMap(choices[0])
	msg := anyMap(choice["message"])
	content := openAIContentToAnthropic(msg["content"])
	for _, value := range anySlice(msg["tool_calls"]) {
		call := anyMap(value)
		fn := anyMap(call["function"])
		var input any = map[string]any{}
		if args := anyString(fn["arguments"]); args != "" {
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				input = map[string]any{"value": args}
			}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": call["id"], "name": fn["name"], "input": input})
	}
	usage := anyMap(in["usage"])
	out := map[string]any{
		"id": firstNonEmpty(anyString(in["id"]), "msg_"+newSessionID()), "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": openAIStopToAnthropic(anyString(choice["finish_reason"])), "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": numberValue(usage["prompt_tokens"]), "output_tokens": numberValue(usage["completion_tokens"])},
	}
	return json.Marshal(out)
}

func anthropicStreamToOpenAI(w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := "chatcmpl_" + newSessionID()
	inputTokens, outputTokens := 0, 0
	toolIndexes := map[int]int{}
	toolCount := 0
	finished := false
	doneSent := false
	emit := func(delta map[string]any, finish any, usage map[string]any) {
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			chunk["usage"] = usage
		}
		writeSSEData(w, chunk, flusher)
	}
	_ = scanSSE(r, func(event string, data []byte) error {
		var payload map[string]any
		if len(data) == 0 || json.Unmarshal(data, &payload) != nil {
			return nil
		}
		switch event {
		case "message_start":
			msg := anyMap(payload["message"])
			if value := anyString(msg["id"]); value != "" {
				id = value
			}
			usage := anyMap(msg["usage"])
			inputTokens = numberValue(usage["input_tokens"])
			emit(map[string]any{"role": "assistant", "content": ""}, nil, nil)
		case "content_block_start":
			blockIndex := numberValue(payload["index"])
			block := anyMap(payload["content_block"])
			if anyString(block["type"]) == "tool_use" {
				toolIndexes[blockIndex] = toolCount
				emit(map[string]any{"tool_calls": []any{map[string]any{"index": toolCount, "id": block["id"], "type": "function", "function": map[string]any{"name": block["name"], "arguments": ""}}}}, nil, nil)
				toolCount++
			} else if text := anyString(block["text"]); text != "" {
				emit(map[string]any{"content": text}, nil, nil)
			}
		case "content_block_delta":
			blockIndex := numberValue(payload["index"])
			delta := anyMap(payload["delta"])
			switch anyString(delta["type"]) {
			case "text_delta":
				emit(map[string]any{"content": anyString(delta["text"])}, nil, nil)
			case "thinking_delta":
				emit(map[string]any{"reasoning_content": anyString(delta["thinking"])}, nil, nil)
			case "input_json_delta":
				emit(map[string]any{"tool_calls": []any{map[string]any{"index": toolIndexes[blockIndex], "function": map[string]any{"arguments": anyString(delta["partial_json"])}}}}, nil, nil)
			}
		case "message_delta":
			delta := anyMap(payload["delta"])
			usage := anyMap(payload["usage"])
			outputTokens = numberValue(usage["output_tokens"])
			oaiUsage := map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}
			emit(map[string]any{}, anthropicStopToOpenAI(anyString(delta["stop_reason"])), oaiUsage)
			finished = true
		case "message_stop":
			if !finished {
				emit(map[string]any{}, "stop", map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens})
				finished = true
			}
			writeSSEDone(w, flusher)
			doneSent = true
		}
		return nil
	})
	if !finished {
		emit(map[string]any{}, "stop", map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens})
	}
	if !doneSent {
		writeSSEDone(w, flusher)
	}
}

func openAIStreamToAnthropic(w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := "msg_" + newSessionID()
	started := false
	textIndex := -1
	nextBlock := 0
	toolBlocks := map[int]int{}
	finishReason := "end_turn"
	inputTokens, outputTokens := 0, 0
	closed := false
	start := func() {
		if started {
			return
		}
		started = true
		writeAnthropicEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}}, flusher)
	}
	closeBlocks := func() {
		if textIndex >= 0 {
			writeAnthropicEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex}, flusher)
			textIndex = -1
		}
		for _, block := range toolBlocks {
			writeAnthropicEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": block}, flusher)
		}
		toolBlocks = map[int]int{}
	}
	finish := func() {
		if closed {
			return
		}
		start()
		closeBlocks()
		writeAnthropicEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": finishReason, "stop_sequence": nil}, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}}, flusher)
		writeAnthropicEvent(w, "message_stop", map[string]any{"type": "message_stop"}, flusher)
		closed = true
	}
	_ = scanSSE(r, func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			finish()
			return nil
		}
		var payload map[string]any
		if len(data) == 0 || json.Unmarshal(data, &payload) != nil {
			return nil
		}
		if value := anyString(payload["id"]); value != "" {
			id = value
		}
		start()
		usage := anyMap(payload["usage"])
		if len(usage) > 0 {
			inputTokens = numberValue(usage["prompt_tokens"])
			outputTokens = numberValue(usage["completion_tokens"])
		}
		choices := anySlice(payload["choices"])
		if len(choices) == 0 {
			return nil
		}
		choice := anyMap(choices[0])
		delta := anyMap(choice["delta"])
		if text := anyString(delta["content"]); text != "" {
			if textIndex < 0 {
				textIndex = nextBlock
				nextBlock++
				writeAnthropicEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": textIndex, "content_block": map[string]any{"type": "text", "text": ""}}, flusher)
			}
			writeAnthropicEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": textIndex, "delta": map[string]any{"type": "text_delta", "text": text}}, flusher)
		}
		for _, value := range anySlice(delta["tool_calls"]) {
			call := anyMap(value)
			toolIndex := numberValue(call["index"])
			fn := anyMap(call["function"])
			blockIndex, exists := toolBlocks[toolIndex]
			if !exists {
				blockIndex = nextBlock
				nextBlock++
				toolBlocks[toolIndex] = blockIndex
				writeAnthropicEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": blockIndex, "content_block": map[string]any{"type": "tool_use", "id": firstNonEmpty(anyString(call["id"]), fmt.Sprintf("toolu_%d", toolIndex)), "name": firstNonEmpty(anyString(fn["name"]), "tool"), "input": map[string]any{}}}, flusher)
			}
			if args := anyString(fn["arguments"]); args != "" {
				writeAnthropicEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}, flusher)
			}
		}
		if reason := anyString(choice["finish_reason"]); reason != "" {
			finishReason = openAIStopToAnthropic(reason)
		}
		return nil
	})
	finish()
}

type pipeResponseWriter struct {
	header http.Header
	pw     *io.PipeWriter
	ready  chan struct{}
	once   sync.Once
	status int
}

func newPipeResponseWriter(pw *io.PipeWriter) *pipeResponseWriter {
	return &pipeResponseWriter{header: make(http.Header), pw: pw, ready: make(chan struct{})}
}

func (w *pipeResponseWriter) Header() http.Header { return w.header }
func (w *pipeResponseWriter) WriteHeader(status int) {
	w.once.Do(func() { w.status = status; close(w.ready) })
}
func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}
func (w *pipeResponseWriter) Flush() { w.WriteHeader(http.StatusOK) }
func (w *pipeResponseWriter) finish() {
	w.WriteHeader(http.StatusOK)
	_ = w.pw.Close()
}
func (w *pipeResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func openAIContentToAnthropic(value any) []any {
	if text, ok := value.(string); ok {
		if text == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": text}}
	}
	var out []any
	for _, item := range anySlice(value) {
		block := anyMap(item)
		switch anyString(block["type"]) {
		case "text", "input_text", "output_text":
			out = append(out, map[string]any{"type": "text", "text": anyString(block["text"])})
		case "image_url":
			image := block["image_url"]
			url := anyString(image)
			if m := anyMap(image); len(m) > 0 {
				url = anyString(m["url"])
			}
			if source := anthropicImageSource(url); source != nil {
				out = append(out, map[string]any{"type": "image", "source": source})
			}
		}
	}
	return out
}

func anthropicContentToOpenAI(value any) []any {
	if text, ok := value.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	var out []any
	for _, item := range anySlice(value) {
		block := anyMap(item)
		switch anyString(block["type"]) {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": anyString(block["text"])})
		case "image":
			source := anyMap(block["source"])
			url := anyString(source["url"])
			if anyString(source["type"]) == "base64" {
				url = "data:" + anyString(source["media_type"]) + ";base64," + anyString(source["data"])
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		case "tool_use", "tool_result":
			out = append(out, block)
		}
	}
	return out
}

func anthropicImageSource(url string) map[string]any {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	if strings.HasPrefix(url, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		if ok && strings.HasSuffix(header, ";base64") {
			return map[string]any{"type": "base64", "media_type": strings.TrimSuffix(header, ";base64"), "data": data}
		}
	}
	return map[string]any{"type": "url", "url": url}
}

func appendAnthropicMessage(messages []any, role string, content []any) []any {
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}
	if len(messages) > 0 {
		last := anyMap(messages[len(messages)-1])
		if anyString(last["role"]) == role {
			last["content"] = append(anySlice(last["content"]), content...)
			messages[len(messages)-1] = last
			return messages
		}
	}
	return append(messages, map[string]any{"role": role, "content": content})
}

func openAIToolsToAnthropic(value any) []any {
	var out []any
	for _, item := range anySlice(value) {
		tool := anyMap(item)
		fn := anyMap(tool["function"])
		if anyString(fn["name"]) == "" {
			continue
		}
		converted := map[string]any{"name": fn["name"], "input_schema": firstNonNil(fn["parameters"], map[string]any{"type": "object", "properties": map[string]any{}})}
		if description := anyString(fn["description"]); description != "" {
			converted["description"] = description
		}
		out = append(out, converted)
	}
	return out
}

func anthropicToolsToOpenAI(value any) []any {
	var out []any
	for _, item := range anySlice(value) {
		tool := anyMap(item)
		if anyString(tool["name"]) == "" {
			continue
		}
		fn := map[string]any{"name": tool["name"], "parameters": firstNonNil(tool["input_schema"], map[string]any{"type": "object", "properties": map[string]any{}})}
		if description := anyString(tool["description"]); description != "" {
			fn["description"] = description
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func openAIToolChoiceToAnthropic(value any) any {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		switch text {
		case "auto", "none":
			return map[string]any{"type": text}
		case "required":
			return map[string]any{"type": "any"}
		}
	}
	choice := anyMap(value)
	fn := anyMap(choice["function"])
	if name := anyString(fn["name"]); name != "" {
		return map[string]any{"type": "tool", "name": name}
	}
	return nil
}

func anthropicToolChoiceToOpenAI(value any) any {
	choice := anyMap(value)
	switch anyString(choice["type"]) {
	case "auto", "none":
		return choice["type"]
	case "any":
		return "required"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]any{"name": choice["name"]}}
	default:
		return nil
	}
}

func openAIContentValue(parts []any) any {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		block := anyMap(parts[0])
		if anyString(block["type"]) == "text" {
			return anyString(block["text"])
		}
	}
	return parts
}

func scanSSE(r io.Reader, fn func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	event := ""
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, []byte(strings.Join(data, "\n")))
		event, data = "", nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := dispatch(); err != nil {
		return err
	}
	return scanner.Err()
}

func writeSSEData(w http.ResponseWriter, value any, flusher http.Flusher) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEDone(w http.ResponseWriter, flusher http.Flusher) {
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeAnthropicEvent(w http.ResponseWriter, event string, value any, flusher http.Flusher) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeAnthropicError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrorType(status), "message": message}})
}

func writeOpenAIErrorAsAnthropic(w http.ResponseWriter, status int, raw []byte) {
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	errBody := anyMap(body["error"])
	message := firstNonEmpty(anyString(errBody["message"]), anyString(body["message"]), strings.TrimSpace(string(raw)), http.StatusText(status))
	writeAnthropicError(w, status, message)
}

func writeAnthropicErrorAsOpenAI(w http.ResponseWriter, status int, raw []byte) {
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	errBody := anyMap(body["error"])
	message := firstNonEmpty(anyString(errBody["message"]), anyString(body["message"]), strings.TrimSpace(string(raw)), http.StatusText(status))
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": firstNonEmpty(anyString(errBody["type"]), "upstream_error")}})
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func anthropicStopToOpenAI(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence", "end_turn", "pause_turn", "refusal":
		return "stop"
	default:
		return firstNonEmpty(reason, "stop")
	}
}

func openAIStopToAnthropic(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if shouldSkipHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isClaudeCodeCompatibleProvider(p ProviderConfig) bool {
	return p.Type == "anthropic" && strings.EqualFold(strings.TrimSpace(providerDataValueGo(p, anthropicRequestModeKey)), claudeCodeCompatibleMode)
}

func anthropicRequestTarget(p ProviderConfig, suffix string) string {
	target := joinURL(p.BaseURL, suffix)
	if !isClaudeCodeCompatibleProvider(p) {
		return target
	}
	if strings.Contains(target, "?") {
		return target + "&beta=true"
	}
	return target + "?beta=true"
}

func applyAnthropicUpstreamHeaders(headers http.Header, incoming *http.Request, p ProviderConfig, key string, stream bool) {
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", acceptHeader(stream))
	headers.Set("x-api-key", key)
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("anthropic-version", anthropicAPIVersion)
	if isClaudeCodeCompatibleProvider(p) {
		headers.Set("anthropic-beta", claudeCodeBetaFeatures)
		headers.Set("anthropic-dangerous-direct-browser-access", "true")
		headers.Set("User-Agent", "claude-cli/2.1.159 (external, sdk-cli)")
		headers.Set("x-app", "cli")
		headers.Set("x-claude-code-session-id", newClaudeCodeSessionID())
		headers.Set("x-stainless-helper-method", "stream")
		headers.Set("x-stainless-retry-count", "0")
		headers.Set("x-stainless-runtime-version", "v24.3.0")
		headers.Set("x-stainless-package-version", "0.94.0")
		headers.Set("x-stainless-runtime", "node")
		headers.Set("x-stainless-lang", "js")
		headers.Set("x-stainless-arch", "x64")
		headers.Set("x-stainless-os", "Windows")
		headers.Set("x-stainless-timeout", "600")
	}
	if incoming == nil {
		return
	}
	if version := strings.TrimSpace(incoming.Header.Get("anthropic-version")); version != "" {
		headers.Set("anthropic-version", version)
	}
	if beta := strings.TrimSpace(incoming.Header.Get("anthropic-beta")); beta != "" {
		headers.Set("anthropic-beta", beta)
	}
	if !strings.HasPrefix(incoming.URL.Path, "/anthropic/") {
		return
	}
	for name, values := range incoming.Header {
		lower := strings.ToLower(name)
		if lower == "user-agent" || lower == "x-app" || lower == "x-claude-code-session-id" || lower == "anthropic-dangerous-direct-browser-access" || strings.HasPrefix(lower, "x-stainless-") {
			headers.Del(name)
			for _, value := range values {
				headers.Add(name, value)
			}
		}
	}
}

func anthropicBlocksContainText(blocks []any, expected string) bool {
	for _, value := range blocks {
		block := anyMap(value)
		if anyString(block["type"]) == "text" && strings.Contains(anyString(block["text"]), expected) {
			return true
		}
	}
	return false
}

func newClaudeCodeSessionID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return newSessionID()
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func copyIfPresent(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

func firstPositiveNumber(a, b any, fallback int) int {
	if value := numberValue(a); value > 0 {
		return value
	}
	if value := numberValue(b); value > 0 {
		return value
	}
	return fallback
}

func numberValue(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		v, _ := n.Int64()
		return int(v)
	default:
		return 0
	}
}

func anyMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func anySlice(value any) []any {
	result, _ := value.([]any)
	return result
}

func anyString(value any) string {
	result, _ := value.(string)
	return result
}

func contentAsText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return ""
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func firstNonNil(value, fallback any) any {
	if value != nil {
		return value
	}
	return fallback
}

func estimateAnthropicInputTokens(body map[string]any) int {
	ascii, nonASCII := countTokenCharacters(body["system"])
	a, n := countTokenCharacters(body["messages"])
	ascii, nonASCII = ascii+a, nonASCII+n
	a, n = countTokenCharacters(body["tools"])
	ascii, nonASCII = ascii+a, nonASCII+n
	tokens := (ascii+3)/4 + nonASCII
	if tokens < 1 {
		return 1
	}
	return tokens
}

func countTokenCharacters(value any) (int, int) {
	switch current := value.(type) {
	case string:
		ascii, nonASCII := 0, 0
		for len(current) > 0 {
			r, size := utf8.DecodeRuneInString(current)
			current = current[size:]
			if r <= 127 {
				ascii++
			} else {
				nonASCII++
			}
		}
		return ascii, nonASCII
	case []any:
		ascii, nonASCII := 0, 0
		for _, item := range current {
			a, n := countTokenCharacters(item)
			ascii, nonASCII = ascii+a, nonASCII+n
		}
		return ascii, nonASCII
	case map[string]any:
		ascii, nonASCII := 0, 0
		for key, item := range current {
			a, n := countTokenCharacters(key)
			ascii, nonASCII = ascii+a, nonASCII+n
			a, n = countTokenCharacters(item)
			ascii, nonASCII = ascii+a, nonASCII+n
		}
		return ascii, nonASCII
	default:
		return 0, 0
	}
}
