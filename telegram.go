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
	"sync"
	"time"
)

type telegramClient struct {
	token   string
	apiBase string
	http    *http.Client

	mu             sync.Mutex
	cachedUsername string
}

const maxPhotoBytes = 20 << 20 // Telegram's getFile download limit.
const typingRefreshInterval = 4 * time.Second

type telegramEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

type telegramError struct {
	Method      string
	Code        int
	Description string
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

func (c *telegramClient) call(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request: %w", method, err)
	}

	url := strings.TrimRight(c.apiBase, "/") + "/bot" + c.token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("telegram %s: read response: %w", method, err)
	}

	var envelope telegramEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram %s: %d: %s", method, response.StatusCode, bodySnippet(raw))
	}
	if !envelope.OK {
		return &telegramError{Method: method, Code: envelope.ErrorCode, Description: envelope.Description}
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

type sendMessageRequest struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID int64  `json:"message_thread_id,omitempty"`
	Text            string `json:"text"`
	ParseMode       string `json:"parse_mode,omitempty"`
}

// send delivers an answer as Telegram HTML, split into as many messages as the
// length limit requires. A chunk Telegram refuses to parse is retried as plain
// text, so a formatting surprise never costs the user their answer.
func (c *telegramClient) send(ctx context.Context, chatID, threadID int64, markdown string) error {
	for _, chunk := range splitTelegramHTML(markdownToHTML(markdown), telegramMessageLimit) {
		err := c.call(ctx, "sendMessage", sendMessageRequest{
			ChatID:          chatID,
			MessageThreadID: threadID,
			Text:            chunk,
			ParseMode:       "HTML",
		}, nil)
		if err == nil {
			continue
		}

		var refused *telegramError
		if !errors.As(err, &refused) || refused.Code != http.StatusBadRequest {
			return err
		}
		plain := htmlToPlain(chunk)
		if plain == "" {
			return err
		}
		log.Printf("telegrambot: chat %d: Telegram rejected the markup (%s), retrying as plain text", chatID, refused.Description)
		if err := c.call(ctx, "sendMessage", sendMessageRequest{
			ChatID:          chatID,
			MessageThreadID: threadID,
			Text:            plain,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *telegramClient) sendChatAction(ctx context.Context, chatID, threadID int64, action string) error {
	return c.call(ctx, "sendChatAction", struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		Action          string `json:"action"`
	}{ChatID: chatID, MessageThreadID: threadID, Action: action}, nil)
}

// keepTyping sends one action immediately and renews it until stop is called.
// stop waits for any in-flight request, so no typing action follows the reply.
func (c *telegramClient) keepTyping(ctx context.Context, chatID, threadID int64, interval time.Duration) func() {
	typingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := c.sendChatAction(typingCtx, chatID, threadID, typingAction); err != nil && typingCtx.Err() == nil {
				log.Printf("telegrambot: typing indicator for chat %d: %s", chatID, strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
			}
			select {
			case <-ticker.C:
			case <-typingCtx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// photoDataURL downloads a Telegram photo and embeds it for DeepSeek. A Telegram
// download URL contains the bot token, so it must not be sent to the model.
func (c *telegramClient) photoDataURL(ctx context.Context, fileID string) (string, error) {
	var file struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := c.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{FileID: fileID}, &file); err != nil {
		return "", fmt.Errorf("telegram: getFile: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	if file.FilePath == "" || file.FileSize > maxPhotoBytes {
		return "", errors.New("telegram: photo is unavailable or too large")
	}

	url := strings.TrimRight(c.apiBase, "/") + "/file/bot" + c.token + "/" + strings.TrimLeft(file.FilePath, "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("telegram: prepare photo download: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("telegram: download photo: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("telegram: download photo: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPhotoBytes+1))
	if err != nil {
		return "", fmt.Errorf("telegram: read photo: %w", err)
	}
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return "", errors.New("telegram: photo is empty or too large")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("telegram: unsupported photo format %q", mime)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// username resolves the bot's own @handle, which is what tells whether a group
// message is addressed to it. Successful lookups are cached for the life of the
// instance; failures are not, so a hiccup does not disable the bot until restart.
func (c *telegramClient) username(ctx context.Context) (string, error) {
	c.mu.Lock()
	cached := c.cachedUsername
	c.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	var me struct {
		Username string `json:"username"`
	}
	if err := c.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return "", err
	}

	c.mu.Lock()
	c.cachedUsername = me.Username
	c.mu.Unlock()
	log.Printf("telegrambot: resolved the bot's own username as @%s", me.Username)
	return me.Username, nil
}
