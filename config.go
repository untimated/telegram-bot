package telegrambot

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Upstream endpoints. Both are overridable so the bot can run against a proxy,
// a compatible gateway, or a fake server in tests.
const (
	defaultTelegramAPIBase = "https://api.telegram.org"
	defaultDeepSeekBaseURL = "https://api.deepseek.com"
	defaultDeepSeekModel   = "deepseek-flash"
	defaultReasoningEffort = "none"
)

const defaultSystemPrompt = "You are a helpful assistant in a Telegram chat. " +
	"Reply in the language the user writes in. Be concise and format answers " +
	"with standard Markdown (bold, lists, inline and fenced code blocks, links)."

// reasoningEfforts mirrors the values accepted by the DeepSeek API. The default
// is "none", which turns thinking mode off: thinking tokens are slow and billed,
// and a chat reply rarely needs them.
var reasoningEfforts = map[string]bool{"none": true, "low": true, "high": true, "max": true}

type config struct {
	telegramToken   string
	telegramAPIBase string
	webhookSecret   string
	allowedChatIDs  map[int64]bool // nil means every chat is allowed
	deepSeekAPIKey  string
	deepSeekBaseURL string
	deepSeekModel   string
	reasoningEffort string
	systemPrompt    string
}

// loadConfig reads the environment on every request so that a misconfigured
// deploy fails with a clear log line instead of an obscure API error.
func loadConfig() (config, error) {
	cfg := config{
		telegramToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		telegramAPIBase: envOrDefault("TELEGRAM_API_BASE", defaultTelegramAPIBase),
		webhookSecret:   os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		deepSeekAPIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		deepSeekBaseURL: envOrDefault("DEEPSEEK_BASE_URL", defaultDeepSeekBaseURL),
		deepSeekModel:   envOrDefault("DEEPSEEK_MODEL", defaultDeepSeekModel),
		reasoningEffort: envOrDefault("DEEPSEEK_REASONING_EFFORT", defaultReasoningEffort),
		systemPrompt:    envOrDefault("SYSTEM_PROMPT", defaultSystemPrompt),
	}

	var missing []string
	if cfg.telegramToken == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}
	if cfg.deepSeekAPIKey == "" {
		missing = append(missing, "DEEPSEEK_API_KEY")
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}
	if !reasoningEfforts[cfg.reasoningEffort] {
		return config{}, fmt.Errorf("DEEPSEEK_REASONING_EFFORT must be one of none, low, high, max (got %q)", cfg.reasoningEffort)
	}

	allowed, err := parseChatIDs(os.Getenv("ALLOWED_CHAT_IDS"))
	if err != nil {
		return config{}, err
	}
	cfg.allowedChatIDs = allowed
	return cfg, nil
}

func (c config) telegram() *telegramClient {
	return &telegramClient{token: c.telegramToken, apiBase: c.telegramAPIBase, http: httpClient}
}

func (c config) deepSeek() *deepSeekClient {
	return &deepSeekClient{
		apiKey:          c.deepSeekAPIKey,
		baseURL:         c.deepSeekBaseURL,
		model:           c.deepSeekModel,
		reasoningEffort: c.reasoningEffort,
		http:            httpClient,
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// parseChatIDs reads the comma-separated ALLOWED_CHAT_IDS allow list. An empty
// value returns nil, which means "no restriction".
func parseChatIDs(raw string) (map[int64]bool, error) {
	ids := make(map[int64]bool)
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_CHAT_IDS: %q is not a chat id", field)
		}
		ids[id] = true
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}
