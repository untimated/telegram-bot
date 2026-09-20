package telegrambot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

const (
	// webhookSecretHeader carries the secret_token handed to setWebhook.
	webhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"
	// replyBudget bounds the work done for one update. Telegram retries an
	// update it has not been answered with a 200, so the budget stays well
	// inside the platform timeout and still leaves room to report a failure.
	replyBudget = 45 * time.Second
	// maxUpdateBytes caps the request body; real Telegram updates are small.
	maxUpdateBytes = 1 << 20
	// maxResponseBytes caps how much of an upstream response we read.
	maxResponseBytes = 1 << 20
	// typingAction is the chat action shown while the model is working.
	typingAction = "typing"
	// maxQuotedUnits caps how much of a replied-to message is quoted back to
	// the model, so one long message cannot dominate the prompt.
	maxQuotedUnits = 1000
)

const helpText = "Hi! I'm a DeepSeek-powered assistant. Send me a message and I'll answer.\n" +
	"In group chats, mention me or reply to one of my messages to get a reply."

// httpClient is shared by both upstream clients so connections are reused
// between the requests one instance handles.
var httpClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}
}()

// update is the part of a Telegram update this bot reacts to.
type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID       int64       `json:"message_id"`
	MessageThreadID int64       `json:"message_thread_id"`
	From            *user       `json:"from"`
	Chat            chat        `json:"chat"`
	Text            string      `json:"text"`
	Caption         string      `json:"caption"`
	Photo           []photoSize `json:"photo"`
	ReplyTo         *message    `json:"reply_to_message"`
	Quote           *textQuote  `json:"quote"`
}

type photoSize struct {
	FileID string `json:"file_id"`
}

// textQuote is the part of a replied-to message the sender selected. Telegram
// also adds quotes by itself, which is what is_manual distinguishes.
type textQuote struct {
	Text     string `json:"text"`
	IsManual bool   `json:"is_manual"`
}

type user struct {
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func init() {
	// The registered name is the function's entry point: keep it in step with
	// --entry-point when deploying.
	functions.HTTP("TelegramHook", TelegramHook)
}

// TelegramHook receives Telegram's webhook calls and answers the messages in
// them with DeepSeek.
func TelegramHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Printf("telegrambot: %v", err)
		http.Error(w, "bot is misconfigured", http.StatusInternalServerError)
		return
	}
	if cfg.webhookSecret != "" {
		got := r.Header.Get(webhookSecretHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(cfg.webhookSecret)) != 1 {
			log.Printf("telegrambot: rejecting update: webhook secret header is missing or does not match TELEGRAM_WEBHOOK_SECRET")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var incoming update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpdateBytes)).Decode(&incoming); err != nil {
		log.Printf("telegrambot: invalid update: %v", err)
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), replyBudget)
	defer cancel()
	// A failure is reported in the chat, not to Telegram: a non-2xx response
	// makes Telegram redeliver the update, which would mean a second model call
	// and a second reply to the user.
	if err := handleUpdate(ctx, cfg, &incoming); err != nil {
		log.Printf("telegrambot: update %d: %v", incoming.UpdateID, err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleUpdate answers one update. Updates that carry nothing to answer are
// ignored.
func handleUpdate(ctx context.Context, cfg config, incoming *update) error {
	msg := incoming.Message
	if msg == nil || msg.Chat.Type == "channel" {
		return nil
	}
	if msg.From != nil && msg.From.IsBot {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	if len(msg.Photo) > 0 {
		text = strings.TrimSpace(msg.Caption)
	}
	photo := msg.Photo
	if len(photo) == 0 && msg.ReplyTo != nil {
		photo = msg.ReplyTo.Photo
	}
	if text == "" && len(photo) == 0 {
		return nil
	}
	log.Printf("telegrambot: update %d received from chat %d (%s)", incoming.UpdateID, msg.Chat.ID, msg.Chat.Type)
	if cfg.allowedChatIDs != nil && !cfg.allowedChatIDs[msg.Chat.ID] {
		log.Printf("telegrambot: ignoring update %d from chat %d, which is not in ALLOWED_CHAT_IDS", incoming.UpdateID, msg.Chat.ID)
		return nil
	}
	isGroup := msg.Chat.Type == "group" || msg.Chat.Type == "supergroup"
	var previous []historyEntry
	if isGroup {
		previous = recentGroupMessages.remember(msg)
	}

	telegram := cfg.telegram()
	if msg.Chat.Type != "private" {
		addressed := false
		if text, addressed = groupText(ctx, telegram, msg, text); !addressed {
			log.Printf("telegrambot: update %d: no mention of the bot, ignoring", incoming.UpdateID)
			return nil
		}
		if text == "" && len(photo) == 0 {
			// The mention was the whole message.
			return telegram.send(ctx, msg.Chat.ID, msg.MessageThreadID, helpText)
		}
	}

	if command, ok := botCommand(text); ok {
		switch command {
		case "/start", "/help":
			return telegram.send(ctx, msg.Chat.ID, msg.MessageThreadID, helpText)
		}
	}

	stopTyping := telegram.keepTyping(ctx, msg.Chat.ID, msg.MessageThreadID, typingRefreshInterval)
	defer stopTyping()

	historyContext := historyPreamble(previous)
	prompt := historyContext + replyPreamble(msg) + text
	userContent := any(prompt)
	if len(photo) > 0 {
		imageURL, err := telegram.photoDataURL(ctx, photo[len(photo)-1].FileID)
		if err != nil {
			stopTyping()
			log.Printf("telegrambot: update %d: %v", incoming.UpdateID, err)
			noticeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return telegram.send(noticeCtx, msg.Chat.ID, msg.MessageThreadID,
				"Sorry, I couldn't read that photo. Please try again in a moment.")
		}
		if text == "" {
			prompt = historyContext + replyPreamble(msg) + "What is in this image?"
		}
		userContent = []chatContentPart{
			{Type: "text", Text: prompt},
			{Type: "image_url", ImageURL: &chatImageURL{URL: imageURL}},
		}
	}

	answer, err := cfg.deepSeek().complete(ctx, []chatMessage{
		{Role: "system", Content: cfg.systemPrompt},
		{Role: "user", Content: userContent},
	})
	stopTyping()
	if err != nil {
		log.Printf("telegrambot: update %d: %v", incoming.UpdateID, err)
		// Report the failure on a context that survives an expired deadline.
		noticeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return telegram.send(noticeCtx, msg.Chat.ID, msg.MessageThreadID,
			"Sorry, I couldn't reach the model just now. Please try again in a moment.")
	}
	if err := telegram.send(ctx, msg.Chat.ID, msg.MessageThreadID, answer); err != nil {
		return err
	}
	if isGroup {
		recentGroupMessages.rememberBot(msg.Chat.ID, msg.MessageThreadID, answer)
	}
	return nil
}

// groupText reports whether a group message is addressed to the bot and returns
// the text to send to the model with the mention removed. Only mentions and
// replies to the bot are answered, so the bot stays quiet during normal chatter.
func groupText(ctx context.Context, telegram *telegramClient, msg *message, text string) (string, bool) {
	if msg.ReplyTo == nil && !strings.Contains(text, "@") {
		return "", false
	}
	username, err := telegram.username(ctx)
	if err != nil {
		// Answering is the better failure mode: if Telegram is unreachable the
		// reply would fail as well.
		log.Printf("telegrambot: resolving the bot username: %v", err)
		return text, true
	}
	if msg.ReplyTo != nil && msg.ReplyTo.From != nil && strings.EqualFold(msg.ReplyTo.From.Username, username) {
		return text, true
	}

	mention := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("@"+username))
	if !mention.MatchString(text) {
		return "", false
	}
	return strings.TrimSpace(mention.ReplaceAllString(text, " ")), true
}

// botCommand returns the /command a message starts with, if any, without the
// @botname suffix Telegram appends in groups.
func botCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	command := text
	if end := strings.IndexAny(command, " \n\t"); end >= 0 {
		command = command[:end]
	}
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	if len(command) < 2 {
		return "", false
	}
	return strings.ToLower(command), true
}

// replyPreamble describes the message being replied to, so that a follow-up like
// "what about the second one?" reaches the model with something to refer to. It
// returns "" when the message is not a reply, or the reply has no text.
func replyPreamble(msg *message) string {
	quoted, author := "", ""

	switch {
	case msg.Quote != nil && msg.Quote.IsManual && strings.TrimSpace(msg.Quote.Text) != "":
		// The sender highlighted part of the original: that part is the point.
		quoted = msg.Quote.Text
		if msg.ReplyTo != nil {
			author = senderName(msg.ReplyTo.From)
		}
	case msg.ReplyTo != nil:
		quoted = firstText(msg.ReplyTo)
		author = senderName(msg.ReplyTo.From)
	}

	quoted = strings.TrimSpace(quoted)
	if quoted == "" {
		return ""
	}
	if author == "" {
		author = "someone"
	}
	if cut := cutAt(quoted, maxQuotedUnits); cut < len(quoted) {
		quoted = strings.TrimSpace(quoted[:cut]) + "…"
	}

	// The quoted text was written by a chat member, so it is fenced off from the
	// instruction that follows.
	return fmt.Sprintf("The user is replying to a message from %s:\n\"\"\"\n%s\n\"\"\"\n\nTheir message:\n", author, quoted)
}

// firstText returns a message's text, or its caption for media messages.
func firstText(msg *message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

// senderName labels the author of a message for the model's benefit.
func senderName(from *user) string {
	switch {
	case from == nil:
		return ""
	case from.IsBot:
		return "you, the assistant"
	case from.Username != "":
		return "@" + from.Username
	default:
		return from.FirstName
	}
}
