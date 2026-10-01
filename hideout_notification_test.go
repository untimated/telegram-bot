package telegrambot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHideoutNotification(t *testing.T) {
	fake := newFakeUpstream(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", testBotToken)
	t.Setenv("TELEGRAM_API_BASE", fake.URL)
	t.Setenv("HIDEOUT_NOTIFICATION_SECRET", "entry-secret")
	t.Setenv("HIDEOUT_NOTIFICATION_CHAT_ID", "-100123")
	t.Setenv("ALLOWED_CHAT_IDS", "-100123")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_REASONING_EFFORT", "invalid")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "different-webhook-secret")

	for _, tc := range []struct {
		name, method, secret, body string
		status                     int
	}{
		{"wrong method", "GET", "entry-secret", `{"name":"@alice"}`, 405},
		{"missing secret", "POST", "", `{"name":"@alice"}`, 401},
		{"wrong secret", "POST", "wrong", `{"name":"@alice"}`, 401},
		{"empty name", "POST", "entry-secret", `{"name":" "}`, 400},
		{"newline", "POST", "entry-secret", `{"name":"Alice\nBob"}`, 400},
		{"malformed", "POST", "entry-secret", `{`, 400},
		{"trailing JSON", "POST", "entry-secret", `{"name":"Alice"}{}`, 400},
		{"arbitrary text", "POST", "entry-secret", `{"name":"Alice","text":"anything"}`, 400},
		{"oversized", "POST", "entry-secret", `{"name":"` + strings.Repeat("x", 2048) + `"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/hideout/entered", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.secret)
			w := httptest.NewRecorder()
			TelegramHook(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if len(fake.paths) != 0 {
		t.Fatalf("rejected requests called upstream: %v", fake.paths)
	}

	for _, name := range []string{"@hermanmichael", "Alice <friend>"} {
		r := httptest.NewRequest("POST", "/hideout/entered", strings.NewReader(`{"name":"`+name+`"}`))
		r.Header.Set("Authorization", "Bearer entry-secret")
		w := httptest.NewRecorder()
		TelegramHook(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	}
	if len(fake.sent) != 2 || len(fake.paths) != 2 || len(fake.deepSeekReqs) != 0 {
		t.Fatalf("expected only two Telegram sends: paths=%v sent=%v", fake.paths, fake.sent)
	}
	for i, want := range []string{"@hermanmichael enters the hideout", "Alice <friend> enters the hideout"} {
		if got := fake.sent[i]; got.ChatID != -100123 || got.Text != want || got.ParseMode != "" {
			t.Fatalf("message = %+v", got)
		}
	}
}

func TestHideoutNotificationConfigurationAndDeliveryFailure(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", testBotToken)
	t.Setenv("HIDEOUT_NOTIFICATION_SECRET", "entry-secret")
	t.Setenv("HIDEOUT_NOTIFICATION_CHAT_ID", "-100123")
	t.Setenv("ALLOWED_CHAT_IDS", "-100999")
	call := func() int {
		r := httptest.NewRequest("POST", "/hideout/entered", strings.NewReader(`{"name":"@alice"}`))
		r.Header.Set("Authorization", "Bearer entry-secret")
		w := httptest.NewRecorder()
		TelegramHook(w, r)
		return w.Code
	}
	if got := call(); got != 503 {
		t.Fatalf("disallowed destination: %d", got)
	}
	t.Setenv("ALLOWED_CHAT_IDS", "-100123")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"unavailable"}`))
	}))
	defer fake.Close()
	t.Setenv("TELEGRAM_API_BASE", fake.URL)
	if got := call(); got != 502 {
		t.Fatalf("delivery failure: %d", got)
	}
	t.Setenv("HIDEOUT_NOTIFICATION_SECRET", "")
	if got := call(); got != 503 {
		t.Fatalf("disabled endpoint: %d", got)
	}
}
