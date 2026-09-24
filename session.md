# TelegramBot — session handoff

Written 2026-09-14, at the end of the session that built the bot. Status: **working in production**, all features verified end to end.

If you are an agent picking this up: read this file, then `function.go`. The operational essentials are also in `.omp/AGENTS.md`, which your session loaded automatically.

---

## 1. What this is

A Go Cloud Function (2nd gen) that Telegram calls via webhook. It answers chat messages with DeepSeek, and can optionally ground answers in Google Search through Gemini.

```
Telegram ──POST update──▶ TelegramHook ──▶ DeepSeek ──▶ reply via sendMessage
                             │                │
                             │                └── tool call: web_search
                             │                        └──▶ Gemini + google_search grounding
                             └── secret check, gating, formatting, splitting
```

Confirmed working: direct messages, group messages, webhook-secret enforcement (200 with the header, 401 without), web search with real citations, and reply-to-message context.

---

## 2. File map

| File | Role |
| --- | --- |
| `function.go` | Webhook entry point, update types, gating, reply context, dispatch |
| `config.go` | Env loading and validation; builds the clients |
| `deepseek.go` | DeepSeek client and the tool-call loop |
| `search.go` | Gemini + Google Search grounding, behind the `searcher` interface |
| `telegram.go` | Bot API calls (`sendMessage`, `sendChatAction`, `getMe`), username cache |
| `format.go` | Markdown → Telegram HTML, and safe splitting at the 4096 limit |
| `function_test.go` | Handler tests + the fake upstream (Telegram, DeepSeek, Gemini in one) |
| `format_test.go`, `search_test.go`, `reply_test.go` | Formatting, search tool, reply context |
| `.env.example` | Every env var the code reads, with its default. Source of truth for config. |

Key symbols: `TelegramHook` (entry), `handleUpdate`, `groupText`, `replyPreamble`, `deepSeekClient.complete`, `deepSeekClient.runTool`, `geminiSearcher.search`, `markdownToHTML`, `splitTelegramHTML`.

---

## 3. Request flow

1. `TelegramHook` rejects non-POST, loads config (500 if required env vars are missing), checks the `X-Telegram-Bot-Api-Secret-Token` header when `TELEGRAM_WEBHOOK_SECRET` is set (401 on mismatch, logged), decodes the update (400 on malformed JSON).
2. `handleUpdate` ignores: no message, channel posts, bot-authored messages, messages with no text, and chats outside `ALLOWED_CHAT_IDS`.
3. Group messages must be **addressed** to the bot: an `@mention`, or a reply to one of the bot's own messages. Otherwise silence (logged). Private chats always answer.
4. `/start` and `/help` (with or without `@botname`) are answered locally, no model call.
5. `replyPreamble` adds the replied-to message as context when there is one.
6. `sendChatAction("typing")`, then `deepSeekClient.complete`.
7. The answer goes back as Telegram HTML; if Telegram refuses the markup, that chunk is retried as plain text.

Failures are reported **in the chat** and the handler still returns 200 — a non-2xx makes Telegram redeliver the update, which would mean a second model call and a duplicate reply.

---

## 4. Configuration

See `.env.example` for the annotated list. Summary:

| Variable | Default | Notes |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | Required. Secret Manager in production. |
| `DEEPSEEK_API_KEY` | — | Required. Secret Manager in production. |
| `TELEGRAM_WEBHOOK_SECRET` | `""` | Empty = header check disabled. Must match `setWebhook`'s `secret_token`. |
| `ALLOWED_CHAT_IDS` | `""` | Empty = every chat, including DMs from strangers. |
| `DEEPSEEK_MODEL` | `deepseek-flash` | `deepseek-v4-pro` is the stronger, pricier option. |
| `DEEPSEEK_REASONING_EFFORT` | `none` | `none` keeps thinking off. `low`/`high`/`max` enable it (slower, billed). |
| `SYSTEM_PROMPT` | built-in | Overrides the default assistant prompt. |
| `GEMINI_API_KEY` | `""` | Empty = the `web_search` tool is not offered at all. |
| `GEMINI_MODEL` | `gemini-3.8-flash` | Any model from Google's grounding-supported list. |
| `TELEGRAM_API_BASE`, `DEEPSEEK_BASE_URL`, `GEMINI_BASE_URL` | real services | Overrides used by tests; leave unset in production. |

---

## 5. Deploy and register

```bash
gcloud functions deploy TelegramHook \
  --gen2 --runtime=go127 --region=YOUR_REGION --source=. \
  --entry-point=TelegramHook --trigger-http --allow-unauthenticated \
  --timeout=120s \
  --set-secrets=TELEGRAM_BOT_TOKEN=telegram-bot-token:latest,DEEPSEEK_API_KEY=deepseek-api-key:latest
```

- The entry point is the name registered in `init()` with `functions.HTTP("TelegramHook", TelegramHook)`. **Changing that string breaks the deploy.**
- The handler gives itself 45s (`replyBudget`); the platform timeout should stay well above it.
- There is no `cmd/main.go`: `gcloud functions deploy` supplies the framework wrapper via buildpacks. Deploying to plain Cloud Run instead would need a small `cmd/server/main.go` calling `funcframework.StartHostPort`.

Registering the webhook, and the ordering rule that matters:

```bash
curl -s "https://api.telegram.org/bot$TELEGRAM_BOT_TOKEN/setWebhook" \
  -d "url=https://YOUR-FUNCTION-URL" \
  -d "secret_token=$TELEGRAM_WEBHOOK_SECRET"
```

Call `setWebhook` **before** setting `TELEGRAM_WEBHOOK_SECRET` on the function. Reversed, every update 401s until Telegram is told otherwise. The secret charset is `A-Za-z0-9_-` only, 1–256 chars (`openssl rand -hex 32` is safe; base64 is not — `+`, `/`, `=` are rejected).

---

## 6. Verifying

```bash
gofmt -l .            # expect no output
go vet ./...
go test ./...         # 35 tests, no credentials needed (fake upstream servers)
```

Secret check, no redeploy needed:

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$FUNCTION_URL" \
  -H 'Content-Type: application/json' -d '{"update_id":1}'          # 401
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$FUNCTION_URL" \
  -H 'Content-Type: application/json' \
  -H "X-Telegram-Bot-Api-Secret-Token: $SECRET" -d '{"update_id":1}' # 200
```

Log lines to grep for, and what they mean:

| Log line | Meaning |
| --- | --- |
| `update N received from chat X (supergroup)` | The update reached the handler. |
| `resolved the bot's own username as @X` | `getMe` succeeded; compare X with what users type. |
| `update N: no mention of the bot, ignoring` | Group gating rejected it; not addressed to the bot. |
| `rejecting update: webhook secret header …` | Secret mismatch between Telegram and the function. |
| `ignoring update N from chat X, which is not in ALLOWED_CHAT_IDS` | The allow list blocked it. |
| `deepseek: searching the web for "…"` | The model asked for a search. |
| `deepseek: web search failed: …` | Grounding failed; the model answered without it. |
| `missing required environment variable(s): …` | Deploy config incomplete. |

`getWebhookInfo` shows the registered URL, `pending_update_count`, and `last_error_message` — the fastest way to see whether Telegram is failing to deliver and why. Note it never echoes the secret back. `getUpdates` returns nothing while a webhook is set.

Known gap: the race detector needs cgo, and this machine has no gcc (`CGO_ENABLED=1 go test -race` fails to build). Shared state is a `sync.Mutex`-guarded username cache and one `http.Client`; a 40-update concurrency check passed without it.

---

## 7. Gotchas, learned the hard way

- **Privacy mode is the big one.** By default, a group bot only receives commands meant for it, replies to its own messages, inline messages, and service messages — **not** plain `@mentions`, despite what BotFather's own description suggests. The fix is BotFather → Bot Settings → Group Privacy → **off**, then **remove and re-add the bot** (the docs require the re-add). This cost a long debugging detour.
- **Failed deliveries are silent.** In the revision before this one, a 401 produced no application log at all, which made "the bot is deaf" look identical to "the webhook is broken".
- **Telegram queues updates for up to 24 hours** and redelivers them. There is no age check yet, so after an outage the bot would answer stale messages (see §9).
- **Message length** is 4096 UTF-16 code units after entity parsing; longer answers are split, and a fenced code block stays balanced across the split.
- **Model names moved on.** `deepseek-chat` is retired; the current names are `deepseek-flash` and `deepseek-v4-pro`. Thinking mode is **on by default** server-side, so it is explicitly disabled here — otherwise every reply pays thinking latency and tokens.
- **Google's classic search API is gone.** Custom Search JSON API is closed to new customers and shuts down 2027-01-01. The replacement is Gemini with the `google_search` tool. Grounding has no free tier, but the first 5,000 searches each month are free, then $14 per 1,000 — and the model may run several queries for one question (observed: two searches for one Bitcoin price question).
- **Tool calls are proposals.** The model cannot execute anything; `runTool` validates the name and arguments. A hallucinated tool name gets `error: unknown tool`, and that text goes back to the model rather than failing the request.

---

## 8. Deliberate design decisions

- **Stateless.** No conversation memory across messages; each turn sees only its own reply chain. Storing history would need Firestore (Cloud Run instances come and go) and the user deferred that. Reply context (§ below) covers the common "what about the second one?" case without storage.
- **Mention gating in groups**, so the bot stays quiet during normal chatter. Note that with privacy mode off the bot receives *every* group message and discards the ones not addressed to it.
- **Failures become text, not errors**, in the tool loop, so a failed search still produces an answer.
- **HTML output with a plain-text fallback**, rather than MarkdownV2 (whose escaping rules lose messages outright when wrong).
- **Web search is opt-in** via `GEMINI_API_KEY`: no key means no tool is offered, and the bot behaves exactly as it did before the feature existed.

---

## 9. Open items, in the order I would do them

1. **Rotate the webhook secret.** It appeared in screenshots during debugging, and it is the only thing stopping forged updates from spending DeepSeek credit.
2. **Set `ALLOWED_CHAT_IDS`** to the friends group id, so a stranger who finds the bot's handle cannot DM it. DMs stop working once set — that is the point, but it is also the fastest sanity check.
3. **Stale-message guard** (~5 lines): ignore updates whose `message.date` is older than a few minutes, so a delivery backlog does not trigger a burst of late answers. Proposed but not implemented.
4. **`fetch_url` tool**: read a page someone pastes; no Google billing. Needs SSRF guards (block private/link-local addresses, cap redirects and body size).
5. **Rolling memory** in Firestore (last ~20 messages per chat, fed as context). The user called this "hard" and deferred it; DeepSeek's prompt caching makes a stable history prefix cheap ($0.003 vs $0.15 per 1M tokens off-peak).
6. **Media handling**: stickers, photos and voice notes are ignored today (only text reaches the model). DeepSeek supports vision only on `deepseek-flash`.

---

## 10. Context for whoever works on this next

Small Indonesian friends group ("Gossip boy"); the bot replies in whatever language the user writes in (DeepSeek handles that; no code needed). The user is on Windows, deploys with `gcloud functions deploy`, prefers concise plain-language explanations, and values simplicity — they pushed back once on the codebase feeling large, and chose to keep the formatting layer rather than lose rendered bold and code blocks.

Working style that worked well here: make a change, run the suite, verify the risky part against the real API where possible (the live DeepSeek calls caught a model-name change and proved the tool protocol), and say plainly what is *not* verified.
