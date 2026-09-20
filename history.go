package telegrambot

import (
	"fmt"
	"strings"
	"sync"
)

const (
	groupHistoryLimit     = 20
	groupHistoryTextLimit = 400
)

type historyKey struct {
	chatID   int64
	threadID int64
}

type historyEntry struct {
	messageID int64 // Zero for the bot's own answers.
	sender    string
	text      string
}

// groupHistory lives only as long as this function instance. Telegram may send
// several webhook requests concurrently, so access to the map is locked.
type groupHistory struct {
	mu       sync.Mutex
	messages map[historyKey][]historyEntry
}

var recentGroupMessages = groupHistory{messages: make(map[historyKey][]historyEntry)}

func (h *groupHistory) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = make(map[historyKey][]historyEntry)
}

// remember returns the preceding messages and then records this one. Recording
// before the mention check lets ordinary group chatter become context later.
func (h *groupHistory) remember(msg *message) []historyEntry {
	key := historyKey{chatID: msg.Chat.ID, threadID: msg.MessageThreadID}
	h.mu.Lock()
	defer h.mu.Unlock()

	previous := append([]historyEntry(nil), h.messages[key]...)
	if msg.MessageID != 0 {
		for i, entry := range previous {
			if entry.messageID == msg.MessageID {
				return append(previous[:i], previous[i+1:]...)
			}
		}
	}

	content := strings.TrimSpace(firstText(msg))
	if len(msg.Photo) > 0 {
		content = strings.TrimSpace("[photo] " + content)
	}
	if content == "" {
		return previous
	}
	sender := senderName(msg.From)
	if sender == "" {
		sender = "member"
	}
	h.append(key, historyEntry{messageID: msg.MessageID, sender: sender, text: shortHistoryText(content)})
	return previous
}

func (h *groupHistory) rememberBot(chatID, threadID int64, answer string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.append(historyKey{chatID: chatID, threadID: threadID}, historyEntry{
		sender: "assistant",
		text:   shortHistoryText(answer),
	})
}

// append must be called with h.mu held.
func (h *groupHistory) append(key historyKey, entry historyEntry) {
	items := append(h.messages[key], entry)
	if len(items) > groupHistoryLimit {
		items = items[len(items)-groupHistoryLimit:]
	}
	h.messages[key] = items
}

func shortHistoryText(value string) string {
	value = strings.TrimSpace(value)
	if cut := cutAt(value, groupHistoryTextLimit); cut < len(value) {
		return strings.TrimSpace(value[:cut]) + "…"
	}
	return value
}

func historyPreamble(entries []historyEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Recent group messages (background context, oldest first):\n")
	for _, entry := range entries {
		fmt.Fprintf(&b, "%q: %q\n", entry.sender, entry.text)
	}
	b.WriteString("\nCurrent message:\n")
	return b.String()
}
