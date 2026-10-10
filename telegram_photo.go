package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// sendPhoto uploads an image as multipart/form-data, which is how Telegram
// accepts files that are not already hosted on its servers or on a public URL.
func (c *telegramClient) sendPhoto(ctx context.Context, chatID, threadID int64, mime string, data []byte) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if threadID != 0 {
		form.WriteField("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	extension := "jpg"
	if strings.Contains(mime, "png") {
		extension = "png"
	}
	file, err := form.CreateFormFile("photo", "image."+extension)
	if err != nil {
		return fmt.Errorf("telegram sendPhoto: %w", err)
	}
	file.Write(data)
	if err := form.Close(); err != nil {
		return fmt.Errorf("telegram sendPhoto: %w", err)
	}

	url := strings.TrimRight(c.apiBase, "/") + "/bot" + c.token + "/sendPhoto"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("telegram sendPhoto: %w", err)
	}
	request.Header.Set("Content-Type", form.FormDataContentType())

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("telegram sendPhoto: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("telegram sendPhoto: read response: %w", err)
	}
	var envelope telegramEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram sendPhoto: %d: %s", response.StatusCode, bodySnippet(raw))
	}
	if !envelope.OK {
		return &telegramError{Method: "sendPhoto", Code: envelope.ErrorCode, Description: envelope.Description}
	}
	return nil
}
