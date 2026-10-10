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

// geminiClient answers conversations with Gemini through the Interactions API.
// With search on, the model runs Google Search itself and cites its sources, so
// no separate tool round trip is needed.
type geminiClient struct {
	apiKey  string
	baseURL string
	model   string
	search  bool
	http    *http.Client
}

func (c *geminiClient) complete(ctx context.Context, messages []chatMessage) (string, error) {
	var system []string
	var input []map[string]any
	for _, message := range messages {
		switch message.Role {
		case "system":
			if text, ok := message.Content.(string); ok {
				system = append(system, text)
			}
		case "user":
			input = append(input, geminiParts(message.Content)...)
		}
	}
	if len(input) == 0 {
		return "", errors.New("gemini: no user content to answer")
	}

	request := map[string]any{"model": c.model, "input": input}
	if len(system) > 0 {
		request["system_instruction"] = strings.Join(system, "\n\n")
	}
	if c.search {
		request["tools"] = []any{map[string]any{"type": "google_search"}}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("gemini: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + geminiSearchPath
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("gemini: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-goog-api-key", c.apiKey)

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("gemini: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("gemini: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return "", fmt.Errorf("gemini: %d %s: %s", response.StatusCode, envelope.Error.Status, envelope.Error.Message)
		}
		return "", fmt.Errorf("gemini: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var interaction geminiInteraction
	if err := json.Unmarshal(body, &interaction); err != nil {
		return "", fmt.Errorf("gemini: decode response: %w", err)
	}
	answer, sources := interaction.findings()
	if answer == "" {
		return "", errors.New("gemini: empty answer")
	}
	log.Printf("gemini: model=%s search=%t sources=%d", c.model, c.search, len(sources))
	return appendSources(answer, interaction.citations()), nil
}

// geminiParts converts a chat message body (plain text, or text plus an image
// data URL) into Interactions API content parts.
func geminiParts(content any) []map[string]any {
	switch value := content.(type) {
	case string:
		return []map[string]any{{"type": "text", "text": value}}
	case []chatContentPart:
		var parts []map[string]any
		for _, part := range value {
			switch {
			case part.Type == "text":
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			case part.ImageURL != nil:
				if mime, data, ok := splitDataURL(part.ImageURL.URL); ok {
					parts = append(parts, map[string]any{"type": "image", "data": data, "mime_type": mime})
				}
			}
		}
		return parts
	}
	return nil
}

// splitDataURL splits "data:<mime>;base64,<data>".
func splitDataURL(url string) (mime, data string, ok bool) {
	rest, found := strings.CutPrefix(url, "data:")
	if !found {
		return "", "", false
	}
	mime, data, found = strings.Cut(rest, ";base64,")
	return mime, data, found && mime != "" && data != ""
}

type citation struct{ title, url string }

// citations lists the distinct pages the answer cites.
func (i geminiInteraction) citations() []citation {
	var out []citation
	seen := make(map[string]bool)
	for _, step := range i.Steps {
		if step.Type != "model_output" {
			continue
		}
		for _, block := range step.Content {
			for _, annotation := range block.Annotations {
				if annotation.Type != "url_citation" || annotation.URL == "" || seen[annotation.URL] {
					continue
				}
				seen[annotation.URL] = true
				out = append(out, citation{title: annotation.Title, url: annotation.URL})
			}
		}
	}
	return out
}

const maxShownSources = 4

// appendSources adds the cited pages as Markdown links under the answer.
func appendSources(answer string, sources []citation) string {
	if len(sources) == 0 {
		return answer
	}
	if len(sources) > maxShownSources {
		sources = sources[:maxShownSources]
	}
	var out strings.Builder
	out.WriteString(answer + "\n\nSources:")
	for _, source := range sources {
		title := strings.NewReplacer("[", "(", "]", ")").Replace(strings.TrimSpace(source.title))
		if title == "" {
			title = source.url
		}
		fmt.Fprintf(&out, "\n- [%s](%s)", title, source.url)
	}
	return out.String()
}
