package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// searcher performs one web search and returns the findings as text the chat
// model can read. A nil searcher means the bot answers from the model's own
// knowledge.
type searcher interface {
	search(ctx context.Context, query string) (string, error)
}

const (
	defaultGeminiBaseURL = "https://generativelanguage.googleapis.com"
	defaultGeminiModel   = "gemini-3.8-flash"
	// geminiSearchPath is the Interactions API. Turning on the built-in Google
	// Search tool lets the model run the queries and cite the pages itself.
	geminiSearchPath = "/v1beta/interactions"
)

type geminiSearcher struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// search asks Gemini with Google Search grounding enabled, and returns its
// grounded answer together with the pages it cited.
func (c *geminiSearcher) search(ctx context.Context, query string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model": c.model,
		"input": query,
		"tools": []any{map[string]any{"type": "google_search"}},
	})
	if err != nil {
		return "", fmt.Errorf("web search: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + geminiSearchPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web search: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-goog-api-key", c.apiKey)

	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("web search: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("web search: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return "", fmt.Errorf("web search: %d %s: %s", response.StatusCode, envelope.Error.Status, envelope.Error.Message)
		}
		return "", fmt.Errorf("web search: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var interaction geminiInteraction
	if err := json.Unmarshal(body, &interaction); err != nil {
		return "", fmt.Errorf("web search: decode response: %w", err)
	}
	answer, sources := interaction.findings()
	if answer == "" {
		return "", errors.New("web search: Gemini returned no grounded answer")
	}
	return formatFindings(query, answer, sources), nil
}

type geminiInteraction struct {
	// OutputText is the flattened answer the SDKs expose; the REST response
	// carries the same text in steps, so it is only a fallback.
	OutputText string `json:"output_text"`
	Steps      []struct {
		Type    string `json:"type"`
		Content []struct {
			Text        string `json:"text"`
			Annotations []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"annotations"`
		} `json:"content"`
	} `json:"steps"`
}

// findings pulls the answer text and the cited pages out of a grounded
// response, ignoring the thinking and search-call steps around them.
func (i geminiInteraction) findings() (string, []string) {
	var answer strings.Builder
	var sources []string
	seen := make(map[string]bool)

	for _, step := range i.Steps {
		if step.Type != "model_output" {
			continue
		}
		for _, block := range step.Content {
			answer.WriteString(block.Text)
			for _, annotation := range block.Annotations {
				if annotation.Type != "url_citation" || annotation.URL == "" || seen[annotation.URL] {
					continue
				}
				seen[annotation.URL] = true
				if annotation.Title != "" {
					sources = append(sources, annotation.Title+" — "+annotation.URL)
				} else {
					sources = append(sources, annotation.URL)
				}
			}
		}
	}

	if text := strings.TrimSpace(answer.String()); text != "" {
		return text, sources
	}
	return strings.TrimSpace(i.OutputText), sources
}

// formatFindings renders a search result as the text the chat model reads.
func formatFindings(query, answer string, sources []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Google Search results for %q:\n\n%s", query, strings.TrimSpace(answer))
	if len(sources) > 0 {
		out.WriteString("\n\nSources:\n")
		for _, source := range sources {
			out.WriteString("- " + source + "\n")
		}
	}
	return strings.TrimSpace(out.String())
}
