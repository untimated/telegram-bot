package telegrambot

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// telegramMessageLimit is the Bot API's cap on one message, counted in UTF-16
// code units of the text that remains after entity parsing.
const telegramMessageLimit = 4096

var (
	htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

	boldPattern      = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)
	strikePattern    = regexp.MustCompile(`(?s)~~(.+?)~~`)
	italicPattern    = regexp.MustCompile(`\*([^*\n]+)\*`)
	headingPattern   = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.+?)[ \t]*$`)
	bulletPattern    = regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+`)
	linkPattern      = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	fenceLanguage    = regexp.MustCompile(`^[A-Za-z0-9+#._-]{1,20}$`)
	tagPattern       = regexp.MustCompile(`<[^>]*>`)
	htmlEntityMarker = regexp.MustCompile(`^&[a-zA-Z0-9#]{1,9};`)
)

// markdownToHTML converts the subset of Markdown the model tends to write into
// the tag set Telegram accepts. Everything else is HTML-escaped first, so model
// output can never inject markup of its own.
func markdownToHTML(markdown string) string {
	escaped := htmlEscaper.Replace(markdown)
	var out strings.Builder

	for i := 0; i < len(escaped); {
		if strings.HasPrefix(escaped[i:], "```") {
			end, next := len(escaped), len(escaped)
			if closing := strings.Index(escaped[i+3:], "```"); closing >= 0 {
				end = i + 3 + closing
				next = end + 3
			}
			out.WriteString(renderCodeBlock(escaped[i+3 : end]))
			i = next
			continue
		}

		if escaped[i] == '`' {
			if closing := strings.IndexByte(escaped[i+1:], '`'); closing >= 0 &&
				!strings.ContainsRune(escaped[i+1:i+1+closing], '\n') {
				out.WriteString("<code>" + escaped[i+1:i+1+closing] + "</code>")
				i += closing + 2
				continue
			}
			// An unmatched backtick is literal text.
			out.WriteString("`")
			i++
			continue
		}

		// Plain text up to the next backtick: inline code is the only construct
		// that must survive without further rewriting.
		next := strings.IndexByte(escaped[i:], '`')
		if next < 0 {
			next = len(escaped) - i
		}
		out.WriteString(renderInline(escaped[i : i+next]))
		i += next
	}
	return out.String()
}

func renderCodeBlock(body string) string {
	language, code := "", body
	if newline := strings.IndexByte(body, '\n'); newline >= 0 {
		if first := strings.TrimSpace(body[:newline]); fenceLanguage.MatchString(first) {
			language = strings.ToLower(first)
			code = body[newline+1:]
		}
	}
	code = strings.Trim(code, "\n")
	if language == "" {
		return "<pre>" + code + "</pre>"
	}
	return `<pre><code class="language-` + language + `">` + code + "</code></pre>"
}

func renderInline(text string) string {
	text = headingPattern.ReplaceAllString(text, "<b>$1</b>")
	text = bulletPattern.ReplaceAllString(text, "• ")
	text = boldPattern.ReplaceAllString(text, "<b>$1</b>")
	text = strikePattern.ReplaceAllString(text, "<s>$1</s>")
	text = linkPattern.ReplaceAllStringFunc(text, renderLink)
	text = italicPattern.ReplaceAllString(text, "<i>$1</i>")
	return text
}

func renderLink(match string) string {
	parts := linkPattern.FindStringSubmatch(match)
	label, target := parts[1], parts[2]
	switch {
	case strings.HasPrefix(target, "https://"),
		strings.HasPrefix(target, "http://"),
		strings.HasPrefix(target, "tg://"):
		return `<a href="` + strings.ReplaceAll(target, `"`, "%22") + `">` + label + `</a>`
	default:
		return match
	}
}

// htmlToPlain strips the markup markdownToHTML added, for the fallback send used
// when Telegram refuses a formatted message.
func htmlToPlain(formatted string) string {
	return html.UnescapeString(tagPattern.ReplaceAllString(formatted, ""))
}

// splitTelegramHTML breaks formatted text into messages of at most limit UTF-16
// code units of visible text, preferring line breaks. Tags left open at a split
// are closed at the end of the chunk and reopened in the next one, so every
// chunk stands on its own.
func splitTelegramHTML(formatted string, limit int) []string {
	if formatted == "" {
		return nil
	}

	var (
		chunks     []string
		current    strings.Builder
		open       []string // full opening tags, so attributes survive a split
		size       int
		hasContent bool
	)

	closeOpen := func() {
		for i := len(open) - 1; i >= 0; i-- {
			name, _, _ := tagInfo(open[i])
			current.WriteString("</" + name + ">")
		}
	}

	flush := func() {
		closeOpen()
		if hasContent {
			chunks = append(chunks, current.String())
		}
		current.Reset()
		size = 0
		hasContent = false
		for _, tag := range open {
			current.WriteString(tag)
			size += len(tag) // tags are ASCII, so bytes equal UTF-16 units
		}
	}

	for _, token := range tokenizeHTML(formatted) {
		switch {
		case token.tag:
			_, closing, selfClosing := tagInfo(token.text)
			// An opening tag that no longer fits starts the next message, so an
			// anchor never ends up separated from the text it labels. A closing
			// tag stays where it is: it belongs to the content before it.
			if !closing && hasContent && size+len(token.text) > limit {
				flush()
			}
			switch {
			case closing:
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			case !selfClosing:
				open = append(open, token.text)
			}
			current.WriteString(token.text)
			size += len(token.text)
		case token.entity:
			if size+utf16Len(token.text) > limit {
				flush()
			}
			current.WriteString(token.text)
			size += utf16Len(token.text)
			hasContent = true
		default:
			text := token.text
			for text != "" {
				if size >= limit {
					flush()
				}
				if size+utf16Len(text) <= limit {
					current.WriteString(text)
					size += utf16Len(text)
					hasContent = true
					break
				}
				cut := cutAt(text, limit-size)
				if cut == 0 {
					flush()
					continue
				}
				current.WriteString(text[:cut])
				size += utf16Len(text[:cut])
				hasContent = true
				text = text[cut:]
				flush()
			}
		}
	}

	if hasContent {
		closeOpen()
		chunks = append(chunks, current.String())
	}
	return chunks
}

// cutAt returns the length in bytes of the longest prefix of s that fits in room
// UTF-16 code units, preferring to break after a newline or a space. It never
// splits a rune.
func cutAt(s string, room int) int {
	if room < 1 {
		return 0
	}
	units, cut := 0, 0
	for cut < len(s) {
		r, width := utf8.DecodeRuneInString(s[cut:])
		inUTF16 := 1
		if r > 0xFFFF {
			inUTF16 = 2
		}
		if units+inUTF16 > room {
			break
		}
		units += inUTF16
		cut += width
	}
	if cut >= len(s) {
		return cut
	}
	if newline := strings.LastIndexByte(s[:cut], '\n'); newline >= 0 {
		return newline + 1
	}
	if space := strings.LastIndexByte(s[:cut], ' '); space > 0 {
		return space + 1
	}
	return cut
}

func utf16Len(s string) int {
	units := 0
	for _, r := range s {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

type htmlToken struct {
	text   string
	tag    bool
	entity bool
}

// tokenizeHTML splits formatted text into tags, entities and text runs, so a
// split never lands inside a tag or an escape sequence.
func tokenizeHTML(formatted string) []htmlToken {
	var tokens []htmlToken
	for i := 0; i < len(formatted); {
		if formatted[i] == '<' {
			if closing := strings.IndexByte(formatted[i:], '>'); closing > 0 {
				tokens = append(tokens, htmlToken{text: formatted[i : i+closing+1], tag: true})
				i += closing + 1
				continue
			}
		}
		if formatted[i] == '&' {
			if match := htmlEntityMarker.FindString(formatted[i:]); match != "" {
				tokens = append(tokens, htmlToken{text: match, entity: true})
				i += len(match)
				continue
			}
		}
		next := strings.IndexAny(formatted[i:], "<&")
		if next < 0 {
			next = len(formatted) - i
		}
		if next == 0 {
			next = 1
		}
		tokens = append(tokens, htmlToken{text: formatted[i : i+next]})
		i += next
	}
	return tokens
}

// tagInfo reports the element name of a tag and whether it closes or is
// self-closing.
func tagInfo(tag string) (name string, closing, selfClosing bool) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">"))
	if strings.HasPrefix(inner, "/") {
		closing = true
		inner = strings.TrimSpace(inner[1:])
	}
	if strings.HasSuffix(inner, "/") {
		selfClosing = true
		inner = strings.TrimSpace(strings.TrimSuffix(inner, "/"))
	}
	if space := strings.IndexAny(inner, " \t"); space >= 0 {
		inner = inner[:space]
	}
	return strings.ToLower(inner), closing, selfClosing
}
