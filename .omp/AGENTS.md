# TelegramBot (D:/Projects/CloudRun/TelegramBot)

Go Cloud Function (gen 2) that Telegram calls by webhook. It answers messages with DeepSeek, and offers the model a `web_search` tool backed by Gemini + Google Search grounding when `GEMINI_API_KEY` is set.

Full history, gotchas, and open items: **`session.md` in the repository root — read it before changing behaviour.**

## Invariants — breaking these breaks production

- The entry point is the name registered in `init()`: `functions.HTTP("TelegramHook", TelegramHook)`. It must match `--entry-point`. Do not rename it.
- `setWebhook` must be called with the secret **before** `TELEGRAM_WEBHOOK_SECRET` is set on the function; reversed, every update 401s. The secret charset is `A-Za-z0-9_-` only.
- A failure must still return **200** with the bad news sent to the chat. A non-2xx makes Telegram redeliver the update, causing a duplicate model call and a duplicate reply.
- Tool failures are returned to the model as **text** (`"error: …"`), never as a Go error — a failed search should still produce an answer.
- Group messages are answered only when addressed (an `@mention`, or a reply to the bot's own message). Private chats always answer.

## Config

`.env.example` lists every variable the code reads, with its default. It is the source of truth; keep it in step with `config.go`.

## Verifying

```bash
gofmt -l .     # expect no output
go vet ./...
go test ./...  # 35 tests, no credentials needed
```

Tests must not make real network calls: `setupBot` in `function_test.go` points every base URL at a fake upstream (Telegram, DeepSeek and Gemini in one server) and sets each relevant variable with `t.Setenv`. Follow that pattern for new tests.

Not runnable here: `go test -race` (needs cgo, and this machine has no gcc).

## Deploy

```bash
gcloud functions deploy TelegramHook --gen2 --runtime=go127 --region=YOUR_REGION \
  --source=. --entry-point=TelegramHook --trigger-http --allow-unauthenticated --timeout=120s
```

The handler budgets 45s for a reply; keep the platform timeout well above it.
