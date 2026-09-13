package telegrambot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "hello there", "hello there"},
		{"markup in the answer is escaped", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"bold", "**important**", "<b>important</b>"},
		{"italic", "say *now*", "say <i>now</i>"},
		{"strikethrough", "~~gone~~", "<s>gone</s>"},
		{"inline code", "run `go test`", "run <code>go test</code>"},
		{"inline code is not reformatted", "`**x**`", "<code>**x**</code>"},
		{"fence keeps its language", "```go\nfmt.Println(\"hi\")\n```", "<pre><code class=\"language-go\">fmt.Println(\"hi\")</code></pre>"},
		{"fence without a language", "```\nplain\n```", "<pre>plain</pre>"},
		{"link", "[docs](https://example.com/a?b=1&c=2)", `<a href="https://example.com/a?b=1&amp;c=2">docs</a>`},
		{"non-http link is left alone", "[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{"snake_case is not italic", "max_tokens and min_tokens", "max_tokens and min_tokens"},
		{"heading becomes bold", "# Title", "<b>Title</b>"},
		{"bullets", "- one\n- two", "• one\n• two"},
		{"multiplication stays readable", "3 * 4 = 12", "3 * 4 = 12"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := markdownToHTML(test.in); got != test.want {
				t.Errorf("markdownToHTML(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestMarkdownToHTMLNeverEmitsBrokenTags(t *testing.T) {
	// Whatever the model writes, every chunk must be parseable: no half-open tag
	// can survive, so Telegram never sees a message it cannot render.
	inputs := []string{
		"**bold", "`unclosed", "```\nunclosed fence", "[link](not a url)", "a < b > c",
		"&amp; already escaped", "~~strike", "*", "```", "`", "text with \"quotes\" and 'apostrophes'",
		"<b>injected</b>", "&#x27;numeric entity&#x27;",
	}
	for _, input := range inputs {
		html := markdownToHTML(input)
		for _, chunk := range splitTelegramHTML(html, 32) {
			assertTagsBalanced(t, input, chunk)
		}
	}
}

func TestSplitTelegramHTMLPreservesTextAndLimit(t *testing.T) {
	var source strings.Builder
	for i := range 400 {
		source.WriteString("**bold ")
		source.WriteString(strings.Repeat("x", i%7+1))
		source.WriteString("** and `code` and a plain sentence that flows on ✨\n\n")
	}
	html := markdownToHTML(source.String())

	chunks := splitTelegramHTML(html, telegramMessageLimit)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want the text split into several messages", len(chunks))
	}
	for i, chunk := range chunks {
		assertTagsBalanced(t, "chunk", chunk)
		if units := utf16Len(htmlToPlain(chunk)); units > telegramMessageLimit {
			t.Errorf("chunk %d carries %d units of text, limit is %d", i, units, telegramMessageLimit)
		}
	}
	// Reopened tags aside, splitting must not lose or duplicate any text.
	joined := htmlToPlain(strings.Join(chunks, ""))
	if want := htmlToPlain(html); joined != want {
		t.Errorf("text changed across the split:\n got %d chars\nwant %d chars", len(joined), len(want))
	}
}

func TestSplitTelegramHTMLReopensCodeBlock(t *testing.T) {
	code := strings.Repeat("x", telegramMessageLimit+10)
	html := markdownToHTML("```\n" + code + "\n```")

	chunks := splitTelegramHTML(html, telegramMessageLimit)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, "<pre>") || !strings.HasSuffix(chunk, "</pre>") {
			t.Errorf("chunk %d = %q…, want the code block closed and reopened", i, chunk[:min(len(chunk), 24)])
		}
	}
	if got := htmlToPlain(chunks[0]) + htmlToPlain(chunks[1]); got != code {
		t.Errorf("code text = %d chars, want %d", len(got), len(code))
	}
}

func TestSplitTelegramHTMLCountsUTF16Units(t *testing.T) {
	// Telegram counts surrogate pairs twice, so a limit of 4 must hold two
	// astral characters and no more.
	chunks := splitTelegramHTML("😀😀😀😀😀", 4)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	for i, chunk := range chunks {
		if units := utf16Len(chunk); units > 4 {
			t.Errorf("chunk %d = %q (%d units), want at most 4", i, chunk, units)
		}
	}
	if joined := strings.Join(chunks, ""); joined != "😀😀😀😀😀" {
		t.Errorf("joined = %q, want the emoji intact", joined)
	}
}

func TestSplitTelegramHTMLKeepsEntitiesWhole(t *testing.T) {
	chunks := splitTelegramHTML("a &amp; b", 6)
	for i, chunk := range chunks {
		if partialEntity(chunk) {
			t.Errorf("chunk %d = %q, want no character reference cut in half", i, chunk)
		}
	}
	if joined := htmlToPlain(strings.Join(chunks, "")); joined != "a & b" {
		t.Errorf("joined = %q, want the reference decoded exactly once", joined)
	}
}

// partialEntity reports whether s holds a '&' that is not part of a complete
// character reference, which is what a split through one leaves behind.
func partialEntity(s string) bool {
	for at := strings.IndexByte(s, '&'); at >= 0; {
		if htmlEntityMarker.FindString(s[at:]) == "" {
			return true
		}
		next := strings.IndexByte(s[at+1:], '&')
		if next < 0 {
			return false
		}
		at += next + 1
	}
	return false
}

func TestParseChatIDs(t *testing.T) {
	if ids, err := parseChatIDs(""); err != nil || ids != nil {
		t.Errorf("parseChatIDs(\"\") = %v, %v; want no restriction", ids, err)
	}
	ids, err := parseChatIDs(" 4242 , -100123 ")
	if err != nil {
		t.Fatalf("parseChatIDs: %v", err)
	}
	if !ids[4242] || !ids[-100123] || len(ids) != 2 {
		t.Errorf("parseChatIDs = %v, want 4242 and -100123", ids)
	}
	if _, err := parseChatIDs("4242,not-a-chat"); err == nil {
		t.Error("parseChatIDs accepted an invalid chat id")
	}
}

// assertTagsBalanced fails when a chunk has a stray closing tag or leaves a tag
// open, either of which makes Telegram reject the message.
func assertTagsBalanced(t *testing.T, label, chunk string) {
	t.Helper()
	var open []string
	for _, token := range tokenizeHTML(chunk) {
		if !token.tag {
			continue
		}
		name, closing, selfClosing := tagInfo(token.text)
		switch {
		case closing:
			if len(open) == 0 || open[len(open)-1] != name {
				t.Fatalf("%q: unexpected closing tag %s in %q", label, token.text, chunk)
			}
			open = open[:len(open)-1]
		case !selfClosing:
			open = append(open, name)
		}
	}
	if len(open) != 0 {
		t.Errorf("%q: unclosed tags %v in %q", label, open, chunk)
	}
}

func TestFakeUpstreamJSONShapesMatchProductionTypes(t *testing.T) {
	// Guards the fake against drifting away from the response shapes the clients
	// decode, which would silently weaken every handler test.
	var completion chatCompletionResponse
	if err := json.Unmarshal([]byte(`{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`), &completion); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "hi" {
		t.Errorf("completion = %+v, want one choice reading hi", completion)
	}
	if completion.Usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v, want the token counts parsed", completion.Usage)
	}
}
