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
	// geminiSearchPath is the Interactions API, used when Gemini is the main
	// model and runs Google Search itself.
	geminiSearchPath = "/v1beta/interactions"
	// deepSeekSearchPath is DeepSeek's own web search API.
	deepSeekSearchPath = "/v1/tools/search"
	searchResultCount  = 5
	maxSearchQueryLen  = 128
)

// deepSeekSearcher backs the web_search tool with DeepSeek's Search API, so
// the DeepSeek key is the only one needed.
type deepSeekSearcher struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

type deepSeekSearchResult struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Summary  string `json:"summary"`
	SiteName string `json:"site_name"`
	Date     string `json:"date"`
}

// search returns the top results, using the summaries DeepSeek writes for
// models to read.
func (c *deepSeekSearcher) search(ctx context.Context, query string) (string, error) {
	// The API rejects queries over 128 characters.
	if runes := []rune(query); len(runes) > maxSearchQueryLen {
		query = string(runes[:maxSearchQueryLen])
	}
	payload, err := json.Marshal(map[string]any{"query": query, "count": searchResultCount})
	if err != nil {
		return "", fmt.Errorf("web search: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + deepSeekSearchPath
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web search: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)

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
		var envelope errorEnvelope
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return "", fmt.Errorf("web search: %d %s: %s", response.StatusCode, envelope.Error.Type, envelope.Error.Message)
		}
		return "", fmt.Errorf("web search: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var decoded struct {
		Results []deepSeekSearchResult `json:"results"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("web search: decode response: %w", err)
	}
	if len(decoded.Results) == 0 {
		return "", errors.New("web search: no results")
	}
	return formatResults(query, decoded.Results), nil
}

// formatResults renders search results as the text the chat model reads.
func formatResults(query string, results []deepSeekSearchResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Web search results for %q:\n", query)
	for i, result := range results {
		fmt.Fprintf(&out, "\n%d. %s — %s\n", i+1, result.Title, result.URL)
		if result.SiteName != "" || result.Date != "" {
			fmt.Fprintf(&out, "   %s %s\n", result.SiteName, result.Date)
		}
		out.WriteString("   " + strings.TrimSpace(result.Summary) + "\n")
	}
	return strings.TrimSpace(out.String())
}

type geminiInteraction struct {
	ID string `json:"id"`
	// OutputText is the flattened answer the SDKs expose; the REST response
	// carries the same text in steps, so it is only a fallback.
	OutputText string `json:"output_text"`
	Steps      []struct {
		Type string `json:"type"`
		// Set on function_call steps.
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Content   []struct {
			Type        string `json:"type"`
			Data        string `json:"data"`
			MimeType    string `json:"mime_type"`
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
