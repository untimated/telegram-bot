package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

var jakartaAndTangerang = []weatherLocation{
	{name: "Jakarta", latitude: "-6.2088", longitude: "106.8456"},
	{name: "Tangerang", latitude: "-6.1783", longitude: "106.6319"},
}

// RunDailyWeatherDigest is the scheduled job entry point.
func RunDailyWeatherDigest(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	chatID, err := dailyChatID()
	if err != nil {
		return err
	}
	if cfg.allowedChatIDs != nil && !cfg.allowedChatIDs[chatID] {
		return fmt.Errorf("DAILY_CHAT_ID %d is not included in ALLOWED_CHAT_IDS", chatID)
	}

	return sendDailyDigest(ctx, cfg, chatID, 0)
}

func sendDailyDigest(ctx context.Context, cfg config, chatID, threadID int64) error {
	answer, err := buildDailyDigest(ctx, cfg)
	if err != nil {
		return err
	}
	if err := cfg.telegram().send(ctx, chatID, threadID, answer); err != nil {
		return fmt.Errorf("send daily digest: %w", err)
	}
	return nil
}

func buildDailyDigest(ctx context.Context, cfg config) (string, error) {
	forecasts := make([]weatherForecast, 0, len(jakartaAndTangerang))
	for _, location := range jakartaAndTangerang {
		forecast, err := fetchWeatherForecast(ctx, location)
		if err != nil {
			return "", err
		}
		forecasts = append(forecasts, forecast)
	}
	if forecasts[0].Date != forecasts[1].Date {
		return "", fmt.Errorf("weather providers returned different local dates: %s and %s", forecasts[0].Date, forecasts[1].Date)
	}

	weatherJSON, err := json.Marshal(forecasts)
	if err != nil {
		return "", fmt.Errorf("encode weather forecast: %w", err)
	}
	prompt := fmt.Sprintf(`Write a compact Bahasa Indonesia weather section for a friends' Telegram digest. The date in Jakarta is %s. Start with "🌦 CUACA". Give one short line for each city with condition, high/low temperature, and maximum rain probability, then one practical activity suggestion. Use only the supplied forecast values; these are daily values, so do not guess rain timing. Do not invent warnings or air-quality readings. Output only the weather section. Forecast JSON: %s`, forecasts[0].Date, weatherJSON)

	deepSeek := cfg.deepSeek()
	deepSeek.search = nil
	weatherText, err := deepSeek.complete(ctx, []chatMessage{
		{Role: "system", Content: "You write concise, practical weather summaries from supplied data only."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return "", fmt.Errorf("write weather digest: %w", err)
	}
	sections := []string{"☀️ *Ringkasan pagi · " + forecasts[0].Date + "*", weatherText + "\nSumber: [Open-Meteo](https://open-meteo.com/en/docs)"}
	if rate, err := fetchUSDtoIDR(ctx); err != nil {
		log.Printf("daily digest: exchange rate unavailable: %v", err)
		sections = append(sections, "💱 *USD/IDR*\nKurs belum tersedia.")
	} else {
		sections = append(sections, formatExchangeRate(rate))
	}
	if headlines, err := fetchTopHeadlines(ctx); err != nil {
		log.Printf("daily digest: headlines unavailable: %v", err)
		sections = append(sections, "📰 *Berita utama ANTARA*\nBelum tersedia.")
	} else {
		sections = append(sections, formatHeadlines(headlines))
	}
	return strings.Join(sections, "\n\n"), nil
}

func dailyChatID() (int64, error) {
	raw := strings.TrimSpace(os.Getenv("DAILY_CHAT_ID"))
	if raw == "" {
		return 0, errors.New("missing required environment variable: DAILY_CHAT_ID")
	}
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || chatID == 0 {
		return 0, fmt.Errorf("DAILY_CHAT_ID must be a non-zero Telegram chat ID (got %q)", raw)
	}
	return chatID, nil
}
