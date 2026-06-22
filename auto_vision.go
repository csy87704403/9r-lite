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
