package telegrambot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testBotToken = "test-token"

// fakeUpstream stands in for api.telegram.org and api.deepseek.com at once:
// both base URLs are pointed at it, and the path says which one was called.
type fakeUpstream struct {
	*httptest.Server

	mu           sync.Mutex
	paths        []string
	deepSeekReqs []map[string]any
	sent         []sentMessage
	reply        string
	deepSeekCode int
	rejectHTML   bool
}

type sentMessage struct {
	ChatID    int64
	Text      string
	ParseMode string
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{reply: "Hello from DeepSeek"}
	fake.Server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)

	switch {
	case r.URL.Path == "/chat/completions":
		f.deepSeekReqs = append(f.deepSeekReqs, body)
		if f.deepSeekCode != 0 {
			w.WriteHeader(f.deepSeekCode)
			writeJSON(w, map[string]any{"error": map[string]any{"message": "model exploded", "type": "server_error"}})
			return
		}
		writeJSON(w, map[string]any{
			"model": "deepseek-flash",
			"choices": []any{map[string]any{
				"finish_reason": "stop",
				"message":       map[string]any{"content": f.reply},
			}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
		})

	case strings.HasSuffix(r.URL.Path, "/getMe"):
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"username": "testbot", "is_bot": true}})

	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		if f.rejectHTML && body["parse_mode"] == "HTML" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities"})
			return
		}
		sent := sentMessage{}
		if id, ok := body["chat_id"].(float64); ok {
			sent.ChatID = int64(id)
		}
		sent.Text, _ = body["text"].(string)
		sent.ParseMode, _ = body["parse_mode"].(string)
		f.sent = append(f.sent, sent)
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})

	default:
		writeJSON(w, map[string]any{"ok": true, "result": true})
	}
}

func (f *fakeUpstream) deepSeekRequests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.deepSeekReqs...)
}

func (f *fakeUpstream) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

func (f *fakeUpstream) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// setupBot points both clients at the fake server. Every relevant variable is
// set explicitly so credentials on the machine running the tests cannot leak in.
func setupBot(t *testing.T, fake *fakeUpstream) {
	t.Helper()
	t.Setenv("TELEGRAM_BOT_TOKEN", testBotToken)
	t.Setenv("TELEGRAM_API_BASE", fake.URL)
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")
	t.Setenv("ALLOWED_CHAT_IDS", "")
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_BASE_URL", fake.URL)
	t.Setenv("DEEPSEEK_MODEL", "")
	t.Setenv("DEEPSEEK_REASONING_EFFORT", "")
	t.Setenv("SYSTEM_PROMPT", "")
}

func postUpdate(t *testing.T, body, secret string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if secret != "" {
		request.Header.Set(webhookSecretHeader, secret)
	}
	recorder := httptest.NewRecorder()
	TelegramHook(recorder, request)
	return recorder
}

func privateUpdate(chatID int64, text string) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":9,"text":%q,`+
		`"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"username":"michael"}}}`,
		text, chatID, chatID)
}

func groupUpdate(text string, replyToBot bool) string {
	reply := ""
	if replyToBot {
		reply = `,"reply_to_message":{"message_id":7,"from":{"id":1,"is_bot":true,"username":"testbot"}}`
	}
	return fmt.Sprintf(`{"update_id":2,"message":{"message_id":9,"text":%q,`+
		`"chat":{"id":-100,"type":"supergroup"},"from":{"id":5,"is_bot":false,"username":"michael"}%s}}`,
		text, reply)
}

func lastUserMessage(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("deepseek request carried no messages: %v", request)
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		t.Fatalf("last message is not an object: %v", messages[len(messages)-1])
	}
	return last
}

func TestPrivateMessageIsAnsweredByModel(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.reply = "Sure — 2+2 is 4."
	setupBot(t, fake)

	if code := postUpdate(t, privateUpdate(4242, "What is 2+2?"), "").Code; code != http.StatusOK {
		t.Fatalf("webhook status = %d, want %d", code, http.StatusOK)
	}

	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("deepseek requests = %d, want 1", len(requests))
	}
	if got := requests[0]["model"]; got != "deepseek-flash" {
		t.Errorf("model = %v, want deepseek-flash", got)
	}
	thinking, _ := requests[0]["thinking"].(map[string]any)
	if got := thinking["type"]; got != "disabled" {
		t.Errorf("thinking.type = %v, want disabled so replies stay fast", got)
	}
	if user := lastUserMessage(t, requests[0]); user["content"] != "What is 2+2?" {
		t.Errorf("prompt = %v, want the user's message", user["content"])
	}

	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("telegram messages sent = %d, want 1", len(sent))
	}
	if sent[0].ChatID != 4242 {
		t.Errorf("chat id = %d, want 4242", sent[0].ChatID)
	}
	if sent[0].ParseMode != "HTML" {
		t.Errorf("parse mode = %q, want HTML", sent[0].ParseMode)
	}
	if !strings.Contains(sent[0].Text, "Sure — 2+2 is 4.") {
		t.Errorf("reply = %q, want the model's answer", sent[0].Text)
	}
}

func TestWebhookSecretIsEnforced(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "s3cret")

	if code := postUpdate(t, privateUpdate(4242, "hi"), "wrong").Code; code != http.StatusUnauthorized {
		t.Errorf("status with a bad secret = %d, want %d", code, http.StatusUnauthorized)
	}
	if fake.requestCount() != 0 {
		t.Errorf("upstream calls = %d, want 0 for an unauthenticated update", fake.requestCount())
	}

	if code := postUpdate(t, privateUpdate(4242, "hi"), "s3cret").Code; code != http.StatusOK {
		t.Errorf("status with the right secret = %d, want %d", code, http.StatusOK)
	}
	if len(fake.deepSeekRequests()) != 1 {
		t.Errorf("deepseek requests = %d, want 1 once the secret matches", len(fake.deepSeekRequests()))
	}
}

func TestUpdatesWithNothingToAnswerAreIgnored(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	for name, body := range map[string]string{
		"no message":   `{"update_id":3}`,
		"sticker":      `{"update_id":4,"message":{"message_id":1,"chat":{"id":4242,"type":"private"},"from":{"id":1,"is_bot":false}}}`,
		"whitespace":   privateUpdate(4242, "   "),
		"bot author":   `{"update_id":5,"message":{"message_id":1,"text":"beep","chat":{"id":4242,"type":"private"},"from":{"id":2,"is_bot":true}}}`,
		"channel":      `{"update_id":6,"message":{"message_id":1,"text":"hello","chat":{"id":-200,"type":"channel"},"from":{"id":3,"is_bot":false}}}`,
		"null message": `{"update_id":7,"message":null}`,
	} {
		if code := postUpdate(t, body, "").Code; code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", name, code, http.StatusOK)
		}
	}
	if fake.requestCount() != 0 {
		t.Errorf("upstream calls = %d, want 0: %v", fake.requestCount(), fake.paths)
	}
}

func TestMalformedUpdateIsRejected(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	if code := postUpdate(t, "{not json", "").Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
	if fake.requestCount() != 0 {
		t.Errorf("upstream calls = %d, want 0", fake.requestCount())
	}
}

func TestGroupMessageNeedsToBeAddressed(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, groupUpdate("just chatting among ourselves", false), "")
	if len(fake.deepSeekRequests()) != 0 || len(fake.sentMessages()) != 0 {
		t.Fatalf("unaddressed group message caused %d model call(s) and %d reply/replies",
			len(fake.deepSeekRequests()), len(fake.sentMessages()))
	}

	postUpdate(t, groupUpdate("@TestBot what's up?", false), "")
	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("deepseek requests = %d, want 1 for a mention", len(requests))
	}
	if got := lastUserMessage(t, requests[0])["content"]; got != "what's up?" {
		t.Errorf("prompt = %q, want the mention stripped", got)
	}

	postUpdate(t, groupUpdate("thanks!", true), "")
	if len(fake.deepSeekRequests()) != 2 {
		t.Errorf("deepseek requests = %d, want 2: a reply to the bot counts as addressed", len(fake.deepSeekRequests()))
	}
}

func TestModelFailureIsReportedToTheUser(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.deepSeekCode = http.StatusInternalServerError
	setupBot(t, fake)

	if code := postUpdate(t, privateUpdate(4242, "hi"), "").Code; code != http.StatusOK {
		t.Fatalf("status = %d, want %d so Telegram does not redeliver", code, http.StatusOK)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("telegram messages sent = %d, want 1 failure notice", len(sent))
	}
	if !strings.Contains(strings.ToLower(sent[0].Text), "try again") {
		t.Errorf("notice = %q, want it to ask the user to retry", sent[0].Text)
	}
}

func TestReplyFallsBackToPlainText(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.rejectHTML = true
	setupBot(t, fake)

	postUpdate(t, privateUpdate(4242, "hi"), "")

	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("telegram messages sent = %d, want 1 plain text fallback", len(sent))
	}
	if sent[0].ParseMode != "" {
		t.Errorf("parse mode = %q, want the fallback to drop markup", sent[0].ParseMode)
	}
	if !strings.Contains(sent[0].Text, "Hello from DeepSeek") {
		t.Errorf("reply = %q, want the answer even when markup is refused", sent[0].Text)
	}
}

func TestCommandsAreAnsweredLocally(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	for _, text := range []string{"/start", "/help", "/Help@testbot"} {
		if code := postUpdate(t, privateUpdate(4242, text), "").Code; code != http.StatusOK {
			t.Errorf("%s: status = %d, want %d", text, code, http.StatusOK)
		}
	}
	if len(fake.deepSeekRequests()) != 0 {
		t.Errorf("deepseek requests = %d, want 0 for commands", len(fake.deepSeekRequests()))
	}
	if sent := fake.sentMessages(); len(sent) != 3 {
		t.Errorf("telegram messages sent = %d, want 3 help replies", len(sent))
	}
}

func TestChatAllowListBlocksOtherChats(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)
	t.Setenv("ALLOWED_CHAT_IDS", "4242, -100")

	postUpdate(t, privateUpdate(4242, "hi"), "")
	if len(fake.deepSeekRequests()) != 1 {
		t.Fatalf("deepseek requests = %d, want 1 for an allowed chat", len(fake.deepSeekRequests()))
	}

	postUpdate(t, privateUpdate(999, "hi"), "")
	if len(fake.deepSeekRequests()) != 1 {
		t.Errorf("deepseek requests = %d, want the blocked chat to cost nothing", len(fake.deepSeekRequests()))
	}
	for _, message := range fake.sentMessages() {
		if message.ChatID != 4242 {
			t.Errorf("a reply went to chat %d, want only the allowed chat 4242", message.ChatID)
		}
	}
}

func TestMissingCredentialsFailFast(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)
	t.Setenv("DEEPSEEK_API_KEY", "")

	if code := postUpdate(t, privateUpdate(4242, "hi"), "").Code; code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", code, http.StatusInternalServerError)
	}
	if fake.requestCount() != 0 {
		t.Errorf("upstream calls = %d, want 0 when the bot is misconfigured", fake.requestCount())
	}
}

func TestLongAnswerIsSplitAcrossMessages(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.reply = strings.Repeat("word ", 3000) // ~15k characters
	setupBot(t, fake)

	postUpdate(t, privateUpdate(4242, "tell me everything"), "")

	sent := fake.sentMessages()
	if len(sent) < 4 {
		t.Fatalf("telegram messages sent = %d, want the answer split over several", len(sent))
	}
	for i, message := range sent {
		if units := utf16Len(htmlToPlain(message.Text)); units > telegramMessageLimit {
			t.Errorf("message %d carries %d units of text, limit is %d", i, units, telegramMessageLimit)
		}
	}
}
