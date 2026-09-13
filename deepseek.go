package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type thinkingMode struct {
	Type string `json:"type"`
}

type chatCompletionRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Thinking        thinkingMode  `json:"thinking"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	Stream          bool          `json:"stream"`
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type deepSeekClient struct {
	apiKey          string
	baseURL         string
	model           string
	reasoningEffort string
	http            *http.Client
}

// complete runs one non-streaming chat completion and returns the assistant's
// answer. DeepSeek is stateless, so the caller supplies the whole conversation.
func (c *deepSeekClient) complete(ctx context.Context, messages []chatMessage) (string, error) {
	request := chatCompletionRequest{
		Model:    c.model,
		Messages: messages,
		Thinking: thinkingMode{Type: "disabled"},
	}
	if c.reasoningEffort != "none" {
		request.Thinking.Type = "enabled"
		request.ReasoningEffort = c.reasoningEffort
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("deepseek: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("deepseek: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Accept", "application/json")

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("deepseek: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("deepseek: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var envelope errorEnvelope
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return "", fmt.Errorf("deepseek: %d %s: %s", response.StatusCode, envelope.Error.Type, envelope.Error.Message)
		}
		return "", fmt.Errorf("deepseek: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return "", fmt.Errorf("deepseek: decode response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("deepseek: response contained no choices")
	}

	choice := completion.Choices[0]
	answer := strings.TrimSpace(choice.Message.Content)
	if answer == "" {
		return "", fmt.Errorf("deepseek: empty answer (finish_reason=%q)", choice.FinishReason)
	}
	log.Printf("deepseek: model=%s finish=%s tokens=%d/%d",
		completion.Model, choice.FinishReason, completion.Usage.PromptTokens, completion.Usage.CompletionTokens)
	return answer, nil
}

// bodySnippet renders a short single-line excerpt of an unexpected response.
func bodySnippet(body []byte) string {
	const maxLength = 300
	excerpt := strings.Join(strings.Fields(string(body)), " ")
	if len(excerpt) > maxLength {
		return excerpt[:maxLength] + "…"
	}
	return excerpt
}
