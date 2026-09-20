package telegrambot

import (
	"fmt"
	"strings"
	"testing"
)

// groupReplyUpdate builds a group message that replies to an earlier one, either
// the bot's or another member's.
func groupReplyUpdate(text string, fromBot bool, repliedText string) string {
	author := `{"id":6,"is_bot":false,"username":"yulianto","first_name":"Yulianto"}`
	if fromBot {
		author = `{"id":1,"is_bot":true,"username":"testbot","first_name":"Prato"}`
	}
	return fmt.Sprintf(`{"update_id":2,"message":{"message_id":9,"text":%q,`+
		`"chat":{"id":-100,"type":"supergroup"},"from":{"id":5,"is_bot":false,"username":"michael"},`+
		`"reply_to_message":{"message_id":7,"from":%s,"text":%q}}}`,
		text, author, repliedText)
}

// quotedReplyUpdate replies to the bot with a highlighted part of its message.
func quotedReplyUpdate(text, quotedText string, manual bool) string {
	return fmt.Sprintf(`{"update_id":2,"message":{"message_id":9,"text":%q,`+
		`"chat":{"id":-100,"type":"supergroup"},"from":{"id":5,"is_bot":false,"username":"michael"},`+
		`"quote":{"text":%q,"is_manual":%t},`+
		`"reply_to_message":{"message_id":7,"from":{"id":1,"is_bot":true,"username":"testbot"},`+
		`"text":"Here are three options:\n1. one\n2. two\n3. three"}}}`,
		text, quotedText, manual)
}

func replyToPhotoUpdate(chatType, text string, fromBot bool) string {
	chatID := int64(4242)
	if chatType != "private" {
		chatID = -100
	}
	author := `{"is_bot":false,"username":"michael"}`
	if fromBot {
		author = `{"is_bot":true,"username":"testbot"}`
	}
	return fmt.Sprintf(`{"update_id":3,"message":{"message_id":9,"text":%q,`+
		`"chat":{"id":%d,"type":%q},"from":{"is_bot":false},`+
		`"reply_to_message":{"message_id":7,"caption":"you can recognize the pic?",`+
		`"photo":[{"file_id":"small"},{"file_id":"quoted-photo"}],"from":%s}}}`,
		text, chatID, chatType, author)
}

// onlyPrompt returns the single prompt the model was asked, failing the test if
// the update never reached it.
func onlyPrompt(t *testing.T, fake *fakeUpstream) string {
	t.Helper()
	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("deepseek requests = %d, want 1", len(requests))
	}
	prompt, _ := lastUserMessage(t, requests[0])["content"].(string)
	return prompt
}

func TestReplyToTheBotsMessageCarriesContext(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	// No mention: replying to the bot is what makes this one addressed.
	postUpdate(t, groupReplyUpdate("what about the second one?", true, "Here are three options: one, two, three."), "")
	prompt := onlyPrompt(t, fake)

	if !strings.Contains(prompt, "Here are three options: one, two, three.") {
		t.Errorf("prompt = %q, want the message being replied to", prompt)
	}
	if !strings.Contains(prompt, "you, the assistant") {
		t.Errorf("prompt = %q, want the quoted message attributed to the bot", prompt)
	}
	if !strings.HasSuffix(prompt, "what about the second one?") {
		t.Errorf("prompt = %q, want the user's own message last", prompt)
	}
}

func TestPrivateTextReplyToPhotoCarriesTheImage(t *testing.T) {
	fake := newFakeUpstream(t)
	setTestPhoto(t, fake)
	setupBot(t, fake)

	postUpdate(t, replyToPhotoUpdate("private", "how about the image I quoted?", false), "")
	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("deepseek requests = %d, want 1", len(requests))
	}
	parts, ok := lastUserMessage(t, requests[0])["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content = %v, want text and quoted photo", lastUserMessage(t, requests[0])["content"])
	}
	textPart := parts[0].(map[string]any)
	if prompt, _ := textPart["text"].(string); !strings.Contains(prompt, "you can recognize the pic?") || !strings.Contains(prompt, "how about the image I quoted?") {
		t.Errorf("prompt = %v, want caption and new question", textPart)
	}
	imagePart := parts[1].(map[string]any)
	imageURL, _ := imagePart["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if imagePart["type"] != "image_url" || !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image part = %v, want quoted photo", imagePart)
	}
}

func TestGroupQuotedPhotoStillNeedsAddressing(t *testing.T) {
	fake := newFakeUpstream(t)
	setTestPhoto(t, fake)
	setupBot(t, fake)

	postUpdate(t, replyToPhotoUpdate("supergroup", "what is this?", false), "")
	if len(fake.deepSeekRequests()) != 0 {
		t.Fatal("unaddressed group reply to a photo reached DeepSeek")
	}
	postUpdate(t, replyToPhotoUpdate("supergroup", "@testbot", false), "")
	requests := fake.deepSeekRequests()
	if len(requests) != 1 {
		t.Fatalf("mentioned group reply made %d model requests, want 1", len(requests))
	}
	parts, ok := lastUserMessage(t, requests[0])["content"].([]any)
	if !ok || len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("user content = %v, want quoted photo", lastUserMessage(t, requests[0])["content"])
	}
}

func TestReplyToAnotherMemberCarriesTheirWords(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, groupReplyUpdate("@testbot translate this", false, "Lu host dimana Kel?"), "")
	prompt := onlyPrompt(t, fake)

	if !strings.Contains(prompt, "Lu host dimana Kel?") {
		t.Errorf("prompt = %q, want the replied-to member's message", prompt)
	}
	if !strings.Contains(prompt, "@yulianto") {
		t.Errorf("prompt = %q, want the member named", prompt)
	}
	if strings.Contains(prompt, "@testbot") {
		t.Errorf("prompt = %q, want the bot's own mention stripped", prompt)
	}
}

func TestManualQuoteIsWhatTheModelSees(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, quotedReplyUpdate("@testbot explain this one", "2. two", true), "")
	prompt := onlyPrompt(t, fake)

	if !strings.Contains(prompt, "2. two") {
		t.Errorf("prompt = %q, want the highlighted part", prompt)
	}
	if strings.Contains(prompt, "1. one") {
		t.Errorf("prompt = %q, want only the highlighted part, not the whole message", prompt)
	}
}

func TestServerAddedQuoteFallsBackToTheWholeMessage(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	// Telegram adds quotes by itself; there the whole message is the better
	// context, since the excerpt was not chosen by the sender.
	postUpdate(t, quotedReplyUpdate("@testbot explain", "1. one", false), "")
	prompt := onlyPrompt(t, fake)

	if !strings.Contains(prompt, "3. three") {
		t.Errorf("prompt = %q, want the whole replied-to message", prompt)
	}
}

func TestReplyWithoutTextAddsNoContext(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	// Replying to a sticker: there is nothing to quote.
	sticker := `{"update_id":2,"message":{"message_id":9,"text":"@testbot what is this",` +
		`"chat":{"id":-100,"type":"supergroup"},"from":{"id":5,"is_bot":false,"username":"michael"},` +
		`"reply_to_message":{"message_id":7,"from":{"id":6,"is_bot":false,"username":"yulianto"},` +
		`"sticker":{"emoji":"😂"}}}}`
	postUpdate(t, sticker, "")

	if prompt := onlyPrompt(t, fake); prompt != "what is this" {
		t.Errorf("prompt = %q, want just the user's message", prompt)
	}
}

func TestLongReplyIsTruncated(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	long := strings.Repeat("word ", 500)
	postUpdate(t, groupReplyUpdate("@testbot summarise that", false, long), "")
	prompt := onlyPrompt(t, fake)

	if !strings.Contains(prompt, "…") {
		t.Errorf("prompt = %q, want the quote marked as truncated", prompt)
	}
	if strings.Contains(prompt, long) {
		t.Error("prompt carried the whole long message, want it capped")
	}
	if len(prompt) > maxQuotedUnits+200 {
		t.Errorf("prompt is %d bytes, want roughly the %d unit cap", len(prompt), maxQuotedUnits)
	}
}

func TestPromptSkipsContextWithoutAReply(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)

	postUpdate(t, privateUpdate(4242, "just a question"), "")

	if prompt := onlyPrompt(t, fake); prompt != "just a question" {
		t.Errorf("prompt = %q, want no preamble without a reply", prompt)
	}
}

func TestSenderNameAndFirstText(t *testing.T) {
	if got := senderName(nil); got != "" {
		t.Errorf("senderName(nil) = %q, want empty", got)
	}
	if got := senderName(&user{IsBot: true, Username: "pratotypebot"}); got != "you, the assistant" {
		t.Errorf("senderName(bot) = %q, want it described as the assistant", got)
	}
	if got := senderName(&user{Username: "yulianto"}); got != "@yulianto" {
		t.Errorf("senderName(handle) = %q, want the handle", got)
	}
	if got := senderName(&user{FirstName: "Yulianto"}); got != "Yulianto" {
		t.Errorf("senderName(name) = %q, want the first name", got)
	}
	if got := firstText(&message{Caption: "a photo caption"}); got != "a photo caption" {
		t.Errorf("firstText(caption) = %q, want the caption", got)
	}
	if got := firstText(&message{Text: "text", Caption: "caption"}); got != "text" {
		t.Errorf("firstText(both) = %q, want the text", got)
	}
}
