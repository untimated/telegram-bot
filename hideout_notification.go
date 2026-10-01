package telegrambot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// hideoutEntered sends a fixed plain-text notice, without loading AI config.
func hideoutEntered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	secret := os.Getenv("HIDEOUT_NOTIFICATION_SECRET")
	if secret == "" {
		http.Error(w, "hideout notifications are not configured", http.StatusServiceUnavailable)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("HIDEOUT_NOTIFICATION_CHAT_ID")), 10, 64)
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if err != nil || chatID >= 0 || token == "" {
		http.Error(w, "hideout notifications are misconfigured", http.StatusServiceUnavailable)
		return
	}
	allowed, err := parseChatIDs(os.Getenv("ALLOWED_CHAT_IDS"))
	if err != nil || (allowed != nil && !allowed[chatID]) {
		http.Error(w, "notification group must be allowed", http.StatusServiceUnavailable)
		return
	}
	var event struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid entry notification", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid entry notification", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(event.Name)
	if name == "" || len([]rune(name)) > 100 || strings.ContainsAny(name, "\r\n") {
		http.Error(w, "invalid player name", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	client := &telegramClient{token: token, apiBase: envOrDefault("TELEGRAM_API_BASE", defaultTelegramAPIBase), http: httpClient}
	if err := client.call(ctx, "sendMessage", sendMessageRequest{ChatID: chatID, Text: name + " enters the hideout"}, nil); err != nil {
		log.Printf("telegrambot: hideout notification: %s", strings.ReplaceAll(err.Error(), token, "[redacted]"))
		http.Error(w, "notification delivery failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
