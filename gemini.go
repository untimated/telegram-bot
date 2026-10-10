package telegrambot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	defaultGeminiImageModel = "gemini-nano-banana-2.1"
	generateImageToolName   = "generate_image"
	// maxToolRounds bounds the function-call round trips for one reply.
	maxToolRounds     = 3
	maxImagesPerReply = 2
)

// generateImageTool lets the model decide when a message is asking for a picture.
var generateImageTool = map[string]any{
	"type": "function",
	"name": generateImageToolName,
	"description": "Generate an image from a text description. Use it when the user asks you to " +
		"draw, create, generate or make a picture, photo, logo, illustration or other image. " +
		"The image is sent to the chat automatically; afterwards reply with at most one short sentence.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "A detailed English description of the image to generate.",
			},
		},
		"required": []string{"prompt"},
	},
}

// generatedImage is an image the model produced for the chat.
type generatedImage struct {
	mime string
	data []byte
}

// geminiReply is a finished answer plus any images to send with it.
type geminiReply struct {
	text   string
	images []generatedImage
}

// geminiClient answers conversations with Gemini through the Interactions API.
// With search on, the model runs Google Search itself and cites its sources, so
// no separate tool round trip is needed. With images on it may also call
// generate_image, which this client runs against the image model.
type geminiClient struct {
	apiKey     string
	baseURL    string
	model      string
	imageModel string
	search     bool
	images     bool
	http       *http.Client
}

func (c *geminiClient) complete(ctx context.Context, messages []chatMessage) (string, error) {
	reply, err := c.completeReply(ctx, messages)
	return reply.text, err
}

// completeReply runs the conversation to a final answer, serving the image
// requests the model makes along the way.
func (c *geminiClient) completeReply(ctx context.Context, messages []chatMessage) (geminiReply, error) {
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
		return geminiReply{}, errors.New("gemini: no user content to answer")
	}

	var tools []any
	if c.search {
		tools = append(tools, map[string]any{"type": "google_search"})
	}
	if c.images {
		tools = append(tools, generateImageTool)
	}

	var reply geminiReply
	previous := ""
	for round := 0; ; round++ {
		request := map[string]any{"model": c.model, "input": input}
		if len(system) > 0 {
			request["system_instruction"] = strings.Join(system, "\n\n")
		}
		if len(tools) > 0 {
			request["tools"] = tools
		}
		if previous != "" {
			request["previous_interaction_id"] = previous
		}

		interaction, err := c.post(ctx, request)
		if err != nil {
			return geminiReply{}, err
		}

		calls := interaction.functionCalls()
		if len(calls) == 0 {
			answer, sources := interaction.findings()
			if answer == "" && len(reply.images) == 0 {
				return geminiReply{}, errors.New("gemini: empty answer")
			}
			log.Printf("gemini: model=%s search=%t sources=%d images=%d", c.model, c.search, len(sources), len(reply.images))
			if answer != "" {
				reply.text = appendSources(answer, interaction.citations())
			}
			return reply, nil
		}
		if round == maxToolRounds || interaction.ID == "" {
			return geminiReply{}, fmt.Errorf("gemini: still asking for tools after %d rounds", round)
		}

		input = nil
		for _, call := range calls {
			input = append(input, map[string]any{
				"type":    "function_result",
				"name":    call.Name,
				"call_id": call.ID,
				"result":  []map[string]any{{"type": "text", "text": c.runTool(ctx, call.Name, call.Arguments, &reply)}},
			})
		}
		previous = interaction.ID
	}
}

// runTool executes a function call and returns the text to hand back to the
// model. Failures come back as text so the model can apologise in its own words.
func (c *geminiClient) runTool(ctx context.Context, name string, arguments json.RawMessage, reply *geminiReply) string {
	if name != generateImageToolName {
		return fmt.Sprintf("error: unknown tool %q", name)
	}
	var args struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil || strings.TrimSpace(args.Prompt) == "" {
		return "error: generate_image needs a non-empty 'prompt' argument"
	}
	if len(reply.images) >= maxImagesPerReply {
		return "error: image limit reached for this reply"
	}
	log.Printf("gemini: generating an image for %q", args.Prompt)
	image, err := c.generateImage(ctx, args.Prompt)
	if err != nil {
		log.Printf("gemini: image generation failed: %v", err)
		return "error: image generation failed (" + err.Error() + "). Tell the user it did not work."
	}
	reply.images = append(reply.images, image)
	return "The image was generated and has been sent to the user."
}

// generateImage asks the image model for one picture.
func (c *geminiClient) generateImage(ctx context.Context, prompt string) (generatedImage, error) {
	interaction, err := c.post(ctx, map[string]any{
		"model":           c.imageModel,
		"input":           []map[string]any{{"type": "text", "text": prompt}},
		"response_format": map[string]any{"type": "image", "mime_type": "image/jpeg"},
	})
	if err != nil {
		return generatedImage{}, err
	}
	for _, step := range interaction.Steps {
		for _, block := range step.Content {
			if block.Type != "image" || block.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(block.Data)
			if err != nil {
				return generatedImage{}, fmt.Errorf("decode image: %w", err)
			}
			mime := block.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			return generatedImage{mime: mime, data: data}, nil
		}
	}
	return generatedImage{}, errors.New("the model returned no image")
}

// post sends one Interactions API request.
func (c *geminiClient) post(ctx context.Context, request map[string]any) (geminiInteraction, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return geminiInteraction{}, fmt.Errorf("gemini: encode request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + geminiSearchPath
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return geminiInteraction{}, fmt.Errorf("gemini: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-goog-api-key", c.apiKey)

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return geminiInteraction{}, fmt.Errorf("gemini: %w", err)
	}
	defer response.Body.Close()

	// Images arrive base64-encoded inside the JSON, so allow more than the
	// usual response cap.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageResponseBytes))
	if err != nil {
		return geminiInteraction{}, fmt.Errorf("gemini: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
			return geminiInteraction{}, fmt.Errorf("gemini: %d %s: %s", response.StatusCode, envelope.Error.Status, envelope.Error.Message)
		}
		return geminiInteraction{}, fmt.Errorf("gemini: %d: %s", response.StatusCode, bodySnippet(body))
	}

	var interaction geminiInteraction
	if err := json.Unmarshal(body, &interaction); err != nil {
		return geminiInteraction{}, fmt.Errorf("gemini: decode response: %w", err)
	}
	return interaction, nil
}

const maxImageResponseBytes = 16 << 20

type geminiFunctionCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// functionCalls lists the function calls the model made.
func (i geminiInteraction) functionCalls() []geminiFunctionCall {
	var calls []geminiFunctionCall
	for _, step := range i.Steps {
		if step.Type == "function_call" {
			calls = append(calls, geminiFunctionCall{ID: step.ID, Name: step.Name, Arguments: step.Arguments})
		}
	}
	return calls
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
