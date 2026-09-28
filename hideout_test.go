package telegrambot

import (
	"net/http"
	"testing"
)

func TestHideoutCommandSendsMiniAppButtonOnlyToAllowedChat(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)
	t.Setenv("ALLOWED_CHAT_IDS", "-100")
	t.Setenv("HIDEOUT_MINI_APP_URL", "https://t.me/TestBot/gossip_hideout")

	for _, command := range []string{"/hideout", "/hideout@TestBot"} {
		if code := postUpdate(t, groupUpdate(command, false), "").Code; code != http.StatusOK {
			t.Fatalf("%s: webhook status = %d", command, code)
		}
	}
	postUpdate(t, groupUpdate("/hideout@otherbot", false), "")
	postUpdate(t, privateUpdate(42, "/hideout"), "")

	sent := fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want two allowed group replies", len(sent))
	}
	for _, message := range sent {
		if message.ChatID != -100 || message.Text != "Enter the Hideout:" {
			t.Fatalf("unexpected reply: %+v", message)
		}
		rows, ok := message.ReplyMarkup["inline_keyboard"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("missing inline keyboard: %+v", message.ReplyMarkup)
		}
		buttons, ok := rows[0].([]any)
		if !ok || len(buttons) != 1 {
			t.Fatalf("missing button: %+v", rows[0])
		}
		button, ok := buttons[0].(map[string]any)
		if !ok || button["url"] != "https://t.me/TestBot/gossip_hideout" {
			t.Fatalf("wrong Mini App button: %+v", buttons[0])
		}
	}
	if len(fake.deepSeekRequests()) != 0 {
		t.Fatal("/hideout called DeepSeek")
	}
}
