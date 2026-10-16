package main

import (
	"encoding/json"
	"strings"
)

func openAIChatHasImage(raw []byte) bool {
	var request struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return false
	}
	for _, message := range request.Messages {
		if messageContentHasImage(message.Content, false) {
			return true
		}
	}
	return false
}

func openAIChatLatestUserHasImage(raw []byte) bool {
	return latestUserTurnHasImage(raw, false)
}

func latestUserTurnHasImage(raw []byte, anthropic bool) bool {
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return false
	}
	hasImage := false
	for i := len(request.Messages) - 1; i >= 0; i-- {
		message := request.Messages[i]
		hasImage = hasImage || messageContentHasImage(message.Content, anthropic)
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") && (!anthropic || !anthropicToolResultOnlyContent(message.Content)) {
			break
		}
	}
	return hasImage
}

func stripOpenAIHistoricalImages(raw []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	messages, _ := request["messages"].([]any)
	changed := false
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			continue
		}
		content, stripped := stripMessageContentImages(message["content"], false)
		message["content"] = content
		changed = changed || stripped
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(request)
}

func anthropicMessagesHaveImage(raw []byte) bool {
	var request struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return false
	}
	for _, message := range request.Messages {
		if messageContentHasImage(message.Content, true) {
			return true
		}
	}
	return false
}

func anthropicMessagesLatestUserHasImage(raw []byte) bool {
	return latestUserTurnHasImage(raw, true)
}

func stripAnthropicHistoricalImages(raw []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	messages, _ := request["messages"].([]any)
	changed := false
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			continue
		}
		content, stripped := stripMessageContentImages(message["content"], true)
		message["content"] = content
		changed = changed || stripped
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(request)
}

func messageContentHasImage(content any, anthropic bool) bool {
	for _, value := range anySlice(content) {
		part := anyMap(value)
		partType := strings.ToLower(strings.TrimSpace(anyString(part["type"])))
		if anthropic {
			if partType == "image" || partType == "image_url" {
				return true
			}
			if partType == "tool_result" && messageContentHasImage(part["content"], true) {
				return true
			}
			continue
		}
		if partType == "image_url" || partType == "input_image" {
			return true
		}
	}
	return false
}

func anthropicToolResultOnlyContent(content any) bool {
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, value := range parts {
		if !strings.EqualFold(strings.TrimSpace(anyString(anyMap(value)["type"])), "tool_result") {
			return false
		}
	}
	return true
}

func stripMessageContentImages(content any, anthropic bool) (any, bool) {
	parts, ok := content.([]any)
	if !ok {
		return content, false
	}
	kept := make([]any, 0, len(parts))
	changed := false
	for _, value := range parts {
		part := anyMap(value)
		partType := strings.ToLower(strings.TrimSpace(anyString(part["type"])))
		if partType == "image_url" || (!anthropic && partType == "input_image") || (anthropic && partType == "image") {
			changed = true
			continue
		}
		if anthropic && partType == "tool_result" {
			stripped, nestedChanged := stripMessageContentImages(part["content"], true)
			if nestedChanged {
				part["content"] = stripped
				changed = true
			}
		}
		kept = append(kept, value)
	}
	if !changed {
		return content, false
	}
	if len(kept) == 0 {
		return "[Historical image omitted; refer to the prior assistant response.]", true
	}
	return kept, true
}
