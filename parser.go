package main

import (
	"encoding/json"
	"io"
	"net/http"
)

type OpenAIRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func ExtractPrompt(r *http.Request) (string, []byte, error) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}

	var openAIReq OpenAIRequest
	if err := json.Unmarshal(bodyBytes, &openAIReq); err != nil {
		return "", bodyBytes, nil
	}

	if len(openAIReq.Messages) > 0 {
		lastMessage := openAIReq.Messages[len(openAIReq.Messages)-1]
		return lastMessage.Content, bodyBytes, nil
	}

	return "", bodyBytes, nil
}
