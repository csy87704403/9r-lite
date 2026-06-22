package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	openAIRequestModeKey = "openaiRequestMode"
	openAIResponsesMode  = "responses"
)

func isResponsesCompatibleProvider(p ProviderConfig) bool {
	return p.Type == "openai" && strings.EqualFold(strings.TrimSpace(providerDataValueGo(p, openAIRequestModeKey)), openAIResponsesMode)
}

func (s *Server) proxyResponsesAsOpenAI(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if strings.TrimSpace(p.BaseURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider base_url is empty"})
		return
	}
	if len(providerAPIKeys(p)) == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provider api_key is empty"})
		return
	}
	body, err := chatCompletionsToResponses(req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp, err := s.doResponsesRequest(r.Context(), p, body, upstreamModel, req.Stream)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	responseModel := providerModelRef(p, upstreamModel)
	if req.Stream || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		responsesStreamToChat(w, resp.Body, responseModel)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	converted, err := responsesResponseToChat(raw, responseModel)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(converted)
}

func (s *Server) doResponsesRequest(ctx context.Context, p ProviderConfig, body []byte, model string, stream bool) (*http.Response, error) {
	keys := providerAPIKeys(p)
	if len(keys) == 0 {
		return nil, errors.New("provider api_key is empty")
	}
	target := joinURL(p.BaseURL, "/responses")
	order := rotatingKeyOrder(p, len(keys), model)
	var lastErr error
	for orderIndex, keyIndex := range order {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+keys[keyIndex])
		req.Header.Set("Accept", acceptHeader(stream))
		req.Header.Set("User-Agent", "9router-lite/0.1")
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			break
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			s.markProviderKeyActive(p.ID, keyIndex, model)
			return resp, nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		if isCredentialKeyError(resp.StatusCode, raw) {
			s.markProviderKeyFailed(p.ID, keyIndex, "", true)
		} else if isQuotaKeyError(resp.StatusCode, raw) {
			s.markProviderKeyFailed(p.ID, keyIndex, model, false)
		} else {
			return resp, nil
		}
		if orderIndex == len(order)-1 {
			return resp, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no usable provider api key")
}

func chatCompletionsToResponses(raw []byte, model string) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := map[string]any{"model": model}
	copyIfPresent(out, in, "stream", "temperature", "top_p", "parallel_tool_calls", "metadata", "user", "service_tier")
	if maxTokens := firstPositiveNumber(in["max_completion_tokens"], in["max_tokens"], 0); maxTokens > 0 {
		out["max_output_tokens"] = maxTokens
	}
	if effort := anyString(in["reasoning_effort"]); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort}
	}

	var instructions []string
	var input []any
	for _, value := range anySlice(in["messages"]) {
		message := anyMap(value)
		role := anyString(message["role"])
		if role == "system" || role == "developer" {
			if text := contentAsText(message["content"]); text != "" {
				instructions = append(instructions, text)
			}
			continue
		}
		if role == "tool" {
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": anyString(message["tool_call_id"]), "output": contentAsText(message["content"]),
			})
			continue
		}
		if role == "" {
			role = "user"
		}
		if content, ok := responsesMessageContent(role, message["content"]); ok {
			input = append(input, map[string]any{"type": "message", "role": role, "content": content})
		}
		if role == "assistant" {
			for _, callValue := range anySlice(message["tool_calls"]) {
				call := anyMap(callValue)
				function := anyMap(call["function"])
				input = append(input, map[string]any{
					"type": "function_call", "call_id": firstNonEmpty(anyString(call["id"]), "call_"+newSessionID()),
					"name": anyString(function["name"]), "arguments": firstNonEmpty(anyString(function["arguments"]), "{}"),
				})
			}
		}
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(input) == 0 {
		out["input"] = ""
	} else {
		out["input"] = input
	}
	if tools := responsesTools(anySlice(in["tools"])); len(tools) > 0 {
		out["tools"] = tools
	}
	if choice, ok := responsesToolChoice(in["tool_choice"]); ok {
		out["tool_choice"] = choice
	}
	if format := responsesTextFormat(anyMap(in["response_format"])); len(format) > 0 {
		out["text"] = map[string]any{"format": format}
	}
	return json.Marshal(out)
}

func responsesMessageContent(role string, value any) (any, bool) {
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, false
		}
		return text, true
	}
	var out []any
	for _, partValue := range anySlice(value) {
		part := anyMap(partValue)
		switch anyString(part["type"]) {
		case "text", "input_text", "output_text":
			partType := "input_text"
			if role == "assistant" {
				partType = "output_text"
			}
			out = append(out, map[string]any{"type": partType, "text": anyString(part["text"])})
		case "image_url", "input_image":
			imageURL := part["image_url"]
			if imageMap, ok := imageURL.(map[string]any); ok {
				imageURL = imageMap["url"]
			}
			if url := anyString(imageURL); url != "" {
				out = append(out, map[string]any{"type": "input_image", "image_url": url})
			}
		}
	}
	return out, len(out) > 0
}

func responsesTools(tools []any) []any {
	var out []any
	for _, value := range tools {
		tool := anyMap(value)
		if anyString(tool["type"]) != "function" {
			continue
		}
		function := anyMap(tool["function"])
		name := anyString(function["name"])
		if name == "" {
			continue
		}
		item := map[string]any{"type": "function", "name": name}
		copyIfPresent(item, function, "description", "parameters", "strict")
		out = append(out, item)
	}
	return out
}

func responsesToolChoice(value any) (any, bool) {
	if text, ok := value.(string); ok && text != "" {
		return text, true
	}
	choice := anyMap(value)
	if anyString(choice["type"]) == "function" {
		name := anyString(anyMap(choice["function"])["name"])
		if name != "" {
			return map[string]any{"type": "function", "name": name}, true
		}
	}
	return nil, false
}

func responsesTextFormat(format map[string]any) map[string]any {
	switch anyString(format["type"]) {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		schema := anyMap(format["json_schema"])
		out := map[string]any{"type": "json_schema"}
		copyIfPresent(out, schema, "name", "schema", "strict", "description")
		return out
	}
	return nil
}

func responsesResponseToChat(raw []byte, model string) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var textParts []string
	var toolCalls []any
	for _, value := range anySlice(response["output"]) {
		item := anyMap(value)
		switch anyString(item["type"]) {
		case "message":
			for _, contentValue := range anySlice(item["content"]) {
				content := anyMap(contentValue)
				if contentType := anyString(content["type"]); contentType == "output_text" || contentType == "text" {
					if text := anyString(content["text"]); text != "" {
						textParts = append(textParts, text)
					}
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, map[string]any{
				"id":       firstNonEmpty(anyString(item["call_id"]), anyString(item["id"]), "call_"+newSessionID()),
				"type":     "function",
				"function": map[string]any{"name": anyString(item["name"]), "arguments": firstNonEmpty(anyString(item["arguments"]), "{}")},
			})
		}
	}
	message := map[string]any{"role": "assistant", "content": strings.Join(textParts, "")}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if len(textParts) == 0 {
			message["content"] = nil
		}
		finishReason = "tool_calls"
	} else if anyString(response["status"]) == "incomplete" {
		finishReason = "length"
	}
	created := numberValue(response["created_at"])
	if created <= 0 {
		created = int(time.Now().Unix())
	}
	out := map[string]any{
		"id": firstNonEmpty(anyString(response["id"]), "chatcmpl_"+newSessionID()), "object": "chat.completion",
		"created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   responsesUsageToChat(anyMap(response["usage"])),
	}
	return json.Marshal(out)
}

func responsesUsageToChat(usage map[string]any) map[string]any {
	input := numberValue(usage["input_tokens"])
	output := numberValue(usage["output_tokens"])
	total := numberValue(usage["total_tokens"])
	if total == 0 {
		total = input + output
	}
	out := map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": total}
	if details := anyMap(usage["input_tokens_details"]); len(details) > 0 {
		out["prompt_tokens_details"] = details
	}
	if details := anyMap(usage["output_tokens_details"]); len(details) > 0 {
		out["completion_tokens_details"] = details
	}
	return out
}

func responsesStreamToChat(w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := "chatcmpl_" + newSessionID()
	created := int(time.Now().Unix())
	roleSent := false
	toolSeen := false
	completed := false
	toolIndexes := map[int]int{}
	nextToolIndex := 0
	emit := func(delta map[string]any, finish any, usage map[string]any) {
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		writeSSEData(w, chunk, flusher)
	}
	ensureRole := func() {
		if !roleSent {
			emit(map[string]any{"role": "assistant", "content": ""}, nil, nil)
			roleSent = true
		}
	}
	_ = scanSSE(r, func(event string, data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil
		}
		eventType := firstNonEmpty(anyString(payload["type"]), event)
		switch eventType {
		case "response.created", "response.in_progress":
			response := anyMap(payload["response"])
			if value := anyString(response["id"]); value != "" {
				id = value
			}
			if value := numberValue(response["created_at"]); value > 0 {
				created = value
			}
		case "response.output_text.delta", "response.refusal.delta":
			ensureRole()
			emit(map[string]any{"content": anyString(payload["delta"])}, nil, nil)
		case "response.output_item.added":
			item := anyMap(payload["item"])
			if anyString(item["type"]) != "function_call" {
				return nil
			}
			ensureRole()
			outputIndex := numberValue(payload["output_index"])
			toolIndex, ok := toolIndexes[outputIndex]
			if !ok {
				toolIndex = nextToolIndex
				nextToolIndex++
				toolIndexes[outputIndex] = toolIndex
			}
			toolSeen = true
			emit(map[string]any{"tool_calls": []any{map[string]any{
				"index": toolIndex, "id": firstNonEmpty(anyString(item["call_id"]), anyString(item["id"])), "type": "function",
				"function": map[string]any{"name": anyString(item["name"]), "arguments": ""},
			}}}, nil, nil)
		case "response.function_call_arguments.delta":
			ensureRole()
			outputIndex := numberValue(payload["output_index"])
			toolIndex, ok := toolIndexes[outputIndex]
			if !ok {
				toolIndex = nextToolIndex
				nextToolIndex++
				toolIndexes[outputIndex] = toolIndex
			}
			toolSeen = true
			emit(map[string]any{"tool_calls": []any{map[string]any{
				"index": toolIndex, "function": map[string]any{"arguments": anyString(payload["delta"])},
			}}}, nil, nil)
		case "response.completed":
			ensureRole()
			response := anyMap(payload["response"])
			finish := "stop"
			if toolSeen {
				finish = "tool_calls"
			} else if anyString(response["status"]) == "incomplete" {
				finish = "length"
			}
			emit(map[string]any{}, finish, responsesUsageToChat(anyMap(response["usage"])))
			writeSSEDone(w, flusher)
			completed = true
		case "response.failed", "error":
			ensureRole()
			message := firstNonEmpty(anyString(anyMap(payload["error"])["message"]), "upstream Responses request failed")
			writeSSEData(w, map[string]any{"error": map[string]any{"message": message, "type": "upstream_error"}}, flusher)
			writeSSEDone(w, flusher)
			completed = true
		}
		return nil
	})
	if !completed {
		ensureRole()
		finish := "stop"
		if toolSeen {
			finish = "tool_calls"
		}
		emit(map[string]any{}, finish, nil)
		writeSSEDone(w, flusher)
	}
}

func parseResponsesModelIDs(r io.Reader) ([]string, error) {
	return parseProtocolModelIDs(r, "responses")
}

func parseMessagesModelIDs(r io.Reader) ([]string, error) {
	return parseProtocolModelIDs(r, "messages")
}

func parseProtocolModelIDs(r io.Reader, protocol string) ([]string, error) {
	var raw any
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	root := raw
	if object, ok := raw.(map[string]any); ok {
		if value, ok := object["data"]; ok {
			root = value
		} else if value, ok := object["models"]; ok {
			root = value
		}
	}
	var ids []string
	for _, value := range anySlice(root) {
		if id, ok := value.(string); ok {
			ids = append(ids, id)
			continue
		}
		model := anyMap(value)
		id := firstNonEmpty(anyString(model["id"]), anyString(model["name"]), anyString(model["model"]))
		if id == "" {
			continue
		}
		apis := anySlice(model["supported_apis"])
		if len(apis) == 0 {
			apis = anySlice(model["supported_protocols"])
		}
		if len(apis) > 0 {
			supported := false
			for _, api := range apis {
				if strings.EqualFold(anyString(api), protocol) {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
		}
		ids = append(ids, id)
	}
	return uniqueStrings(ids), nil
}
