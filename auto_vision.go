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
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return false
	}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(request.Messages[i].Role), "user") {
			return messageContentHasImage(request.Messages[i].Content, false)
		}
	}
	return false
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
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(parts))
		for _, partValue := range parts {
			part, _ := partValue.(map[string]any)
			partType := strings.ToLower(strings.TrimSpace(anyString(part["type"])))
			if partType == "image_url" || partType == "input_image" {
				changed = true
				continue
			}
			kept = append(kept, partValue)
		}
		if len(kept) == 0 {
			message["content"] = "[Historical image omitted; refer to the prior assistant response.]"
		} else {
			message["content"] = kept
		}
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

func messageContentHasImage(content any, anthropic bool) bool {
	for _, value := range anySlice(content) {
		part := anyMap(value)
		partType := strings.ToLower(strings.TrimSpace(anyString(part["type"])))
		if anthropic {
			if partType == "image" || partType == "image_url" {
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
