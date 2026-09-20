package telegrambot

import (
	"fmt"
	"strings"
	"testing"
)

func groupHistoryUpdate(id, chatID, threadID int64, username, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"message_thread_id":%d,"text":%q,`+
		`"chat":{"id":%d,"type":"supergroup"},"from":{"is_bot":false,"username":%q}}}`,
		id, id, threadID, text, chatID, username)
}

func TestUnmentionedGroupMessagesBecomeContext(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, groupHistoryUpdate(1, -100, 0, "alice", "We chose the 2 PK model."), "")
	postUpdate(t, groupHistoryUpdate(2, -100, 0, "bob", "The budget is five million."), "")
	postUpdate(t, groupHistoryUpdate(3, -200, 0, "stranger", "This belongs to another group."), "")
	if len(fake.deepSeekRequests()) != 0 || len(fake.sentMessages()) != 0 {
		t.Fatal("unmentioned group chatter caused a model call or reply")
	}
	if fake.requestCount() != 0 {
		t.Errorf("unmentioned group chatter made %d upstream requests, want none", fake.requestCount())
	}

	postUpdate(t, groupHistoryUpdate(4, -100, 0, "michael", "@testbot what did we choose?"), "")
	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	prompt, _ := lastUserMessage(t, requests[0])["content"].(string)
	for _, want := range []string{"@alice", "We chose the 2 PK model.", "@bob", "The budget is five million.", "what did we choose?"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q: %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "This belongs to another group.") || strings.Contains(prompt, "@testbot") {
		t.Errorf("prompt mixed chats or kept the bot mention: %q", prompt)
	}

	postUpdate(t, groupHistoryUpdate(5, -100, 0, "michael", "@testbot what was your answer?"), "")
	requests = fake.deepSeekRequests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	prompt, _ = lastUserMessage(t, requests[1])["content"].(string)
	if !strings.Contains(prompt, `"assistant": "Hello from DeepSeek"`) {
		t.Errorf("follow-up prompt lacks the bot's earlier answer: %q", prompt)
	}
}

func TestGroupHistoryKeepsOnlyTwentyMessagesPerTopic(t *testing.T) {
	history := groupHistory{messages: make(map[historyKey][]historyEntry)}
	for id := int64(1); id <= 25; id++ {
		history.remember(&message{
			MessageID:       id,
			MessageThreadID: 7,
			Chat:            chat{ID: -100, Type: "supergroup"},
			Text:            fmt.Sprintf("message %d", id),
		})
	}
	key := historyKey{chatID: -100, threadID: 7}
	if got := len(history.messages[key]); got != groupHistoryLimit {
		t.Fatalf("stored messages = %d, want %d", got, groupHistoryLimit)
	}
	if history.messages[key][0].messageID != 6 || history.messages[key][19].messageID != 25 {
		t.Errorf("wrong window: first=%d last=%d", history.messages[key][0].messageID, history.messages[key][19].messageID)
	}
	history.remember(&message{MessageID: 25, MessageThreadID: 7, Chat: chat{ID: -100}, Text: "duplicate"})
	if len(history.messages[key]) != groupHistoryLimit || history.messages[key][19].text != "message 25" {
		t.Error("duplicate Telegram delivery changed the history")
	}
	otherTopic := history.remember(&message{MessageID: 26, MessageThreadID: 8, Chat: chat{ID: -100}, Text: "different topic"})
	if len(otherTopic) != 0 {
		t.Errorf("another topic saw %d prior messages", len(otherTopic))
	}
}
