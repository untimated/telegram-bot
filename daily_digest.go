package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var jakartaAndTangerang = []weatherLocation{
	{name: "Jakarta", latitude: "-6.2088", longitude: "106.8456"},
	{name: "Tangerang", latitude: "-6.1783", longitude: "106.6319"},
}

// RunDailyWeatherDigest fetches today's forecasts, asks DeepSeek for practical
// activity advice, and sends the digest to DAILY_CHAT_ID.
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

	forecasts := make([]weatherForecast, 0, len(jakartaAndTangerang))
	for _, location := range jakartaAndTangerang {
		forecast, err := fetchWeatherForecast(ctx, location)
		if err != nil {
			return err
		}
		forecasts = append(forecasts, forecast)
	}
	if forecasts[0].Date != forecasts[1].Date {
		return fmt.Errorf("weather providers returned different local dates: %s and %s", forecasts[0].Date, forecasts[1].Date)
	}

	weatherJSON, err := json.MarshalIndent(forecasts, "", "  ")
	if err != nil {
		return fmt.Errorf("encode weather forecast: %w", err)
	}
	prompt := fmt.Sprintf(`Prepare a concise, friendly daily weather digest in Bahasa Indonesia for our friends group. Start with a short heading that includes the date.
Today in Jakarta local time is %s. Compare Jakarta and Tangerang. For each city, report the condition, high and low temperature in Celsius, feels-like high, maximum rain probability, expected rainfall in millimeters, and maximum wind speed in km/h.
Then give two or three practical suggestions for today's activities, commuting, or what to bring, based only on this forecast. Do not guess what time rain will occur; these are daily summary values. Do not invent data, air-quality readings, or warnings. Keep the advice useful and calm. The forecast data is JSON:
%s`, forecasts[0].Date, weatherJSON)

	deepSeek := cfg.deepSeek()
	deepSeek.search = nil // The structured forecast is the source of truth for this digest.
	answer, err := deepSeek.complete(ctx, []chatMessage{
		{Role: "system", Content: "You write concise, practical daily weather summaries. Use only the supplied forecast values."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("write weather digest: %w", err)
	}
	answer += "\n\nSumber prakiraan: [Open-Meteo](https://open-meteo.com/en/docs)."

	if err := cfg.telegram().send(ctx, chatID, 0, answer); err != nil {
		return fmt.Errorf("send weather digest: %w", err)
	}
	return nil
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
