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

const (
	webSearchToolName = "web_search"
	// maxSearchRounds bounds how many times the model may look something up
	// before it has to answer with what it already has.
	maxSearchRounds = 3
)

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolDefinition struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// webSearchTool lets the model decide for itself when it needs the live web. It
// is only offered when a searcher is configured.
var webSearchTool = toolDefinition{
	Type: "function",
	Function: toolFunction{
		Name: webSearchToolName,
		Description: "Search Google for anything current: news, sports results, prices, " +
			"weather, releases, or facts after your knowledge cutoff. Returns an answer with its sources.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query to look up.",
				},
			},
			"required": []string{"query"},
		},
	},
}

type thinkingMode struct {
	Type string `json:"type"`
}

type chatCompletionRequest struct {
	Model           string           `json:"model"`
	Messages        []chatMessage    `json:"messages"`
	Thinking        thinkingMode     `json:"thinking"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Tools           []toolDefinition `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
}

type chatChoice struct {
	FinishReason string      `json:"finish_reason"`
	Message      chatMessage `json:"message"`
}

type chatCompletionResponse struct {
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   struct {
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
	search          searcher // nil disables the web_search tool
	http            *http.Client
}

// complete runs the conversation to a final answer, executing the web searches
// the model asks for along the way.
func (c *deepSeekClient) complete(ctx context.Context, messages []chatMessage) (string, error) {
	var tools []toolDefinition
	if c.search != nil {
		tools = []toolDefinition{webSearchTool}
	}

	for round := 0; ; round++ {
		completion, err := c.send(ctx, messages, tools)
		if err != nil {
			return "", err
		}
		if len(completion.Choices) == 0 {
			return "", errors.New("deepseek: response contained no choices")
		}

		choice := completion.Choices[0]
		if len(choice.Message.ToolCalls) == 0 {
			answer, _ := choice.Message.Content.(string)
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return "", fmt.Errorf("deepseek: empty answer (finish_reason=%q)", choice.FinishReason)
			}
			log.Printf("deepseek: model=%s finish=%s tokens=%d/%d",
				completion.Model, choice.FinishReason, completion.Usage.PromptTokens, completion.Usage.CompletionTokens)
			return answer, nil
		}
		if round == maxSearchRounds {
			return "", fmt.Errorf("deepseek: still asking for tools after %d rounds", round)
		}

		// Feed the assistant's tool calls and their results back in.
		if choice.Message.Content == nil {
			choice.Message.Content = ""
		}
		messages = append(messages, choice.Message)
		for _, call := range choice.Message.ToolCalls {
			messages = append(messages, chatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    c.runTool(ctx, call),
			})
		}
	}
}

// send performs one request and returns the decoded completion.
func (c *deepSeekClient) send(ctx context.Context, messages []chatMessage, tools []toolDefinition) (chatCompletionResponse, error) {
	request := chatCompletionRequest{
		Model:    c.model,
		Messages: messages,
		Thinking: thinkingMode{Type: "disabled"},
		Tools:    tools,
	}
	if c.reasoningEffort != "none" {
		request.Thinking.Type = "enabled"
		request.ReasoningEffort = c.reasoningEffort
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("deepseek: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("deepseek: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Accept", "application/json")

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("deepseek: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return chatCompletionResponse{}, fmt.Errorf("deepseek: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var envelope errorEnvelope
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return chatCompletionResponse{}, fmt.Errorf("deepseek: %d %s: %s", response.StatusCode, envelope.Error.Type, envelope.Error.Message)
		}
		return chatCompletionResponse{}, fmt.Errorf("deepseek: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return chatCompletionResponse{}, fmt.Errorf("deepseek: decode response: %w", err)
	}
	return completion, nil
}

// runTool executes a tool call from the model and returns the text to feed back.
// A failure comes back as text rather than an error, so the model can answer
// without the lookup instead of the whole reply failing.
func (c *deepSeekClient) runTool(ctx context.Context, call toolCall) string {
	if call.Function.Name != webSearchToolName {
		return fmt.Sprintf("error: unknown tool %q", call.Function.Name)
	}
	if c.search == nil {
		return "error: web search is not configured"
	}

	var arguments struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return fmt.Sprintf("error: could not read the tool arguments: %v", err)
	}
	query := strings.TrimSpace(arguments.Query)
	if query == "" {
		return "error: the search tool needs a non-empty 'query' argument"
	}

	log.Printf("deepseek: searching the web for %q", query)
	findings, err := c.search.search(ctx, query)
	if err != nil {
		log.Printf("deepseek: web search failed: %v", err)
		return "error: the web search failed: " + err.Error()
	}
	return findings
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
