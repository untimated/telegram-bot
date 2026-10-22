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
	"time"
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

	answer, err := buildDailyDigest(ctx, cfg)
	if err != nil {
		log.Printf("daily digest unavailable: %v", err)
		message := "Today's Digest is not available. Issues with: " + err.Error()
		noticeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if sendErr := cfg.telegram().send(noticeCtx, chatID, 0, message); sendErr != nil {
			return fmt.Errorf("daily digest failed: %v; send failure notice: %w", err, sendErr)
		}
		return nil
	}
	if err := cfg.telegram().send(ctx, chatID, 0, answer); err != nil {
		return fmt.Errorf("send daily digest: %w", err)
	}
	return nil
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
	weatherText, date := buildWeatherSection(ctx, cfg)
	if date == "" {
		date = time.Now().In(time.FixedZone("WIB", 7*60*60)).Format("2006-01-02")
	}
	sections := []string{weatherText}
	if rate, err := fetchUSDtoIDR(ctx); err != nil {
		log.Printf("daily digest: exchange rate unavailable: %v", err)
		sections = append(sections, "💱 *USD/IDR*\nKurs belum tersedia.")
	} else {
		financeText := formatExchangeRate(rate)
		if rates, err := fetchUSDIDRWeek(ctx, rate.Date); err != nil {
			log.Printf("daily digest: weekly exchange rates unavailable: %v", err)
			financeText += "\nTren mingguan belum tersedia."
		} else {
			financeText += fmt.Sprintf("\n7 hari (%s–%s): %+.2f%%", rates[0].Date, rates[len(rates)-1].Date,
				(rates[len(rates)-1].Rate/rates[0].Rate-1)*100)
			if summary, err := summarizeExchangeWeek(ctx, cfg, rates); err != nil {
				log.Printf("daily digest: weekly exchange summary unavailable: %v", err)
			} else {
				financeText += "\n" + summary
			}
		}
		sections = append(sections, financeText)
	}
	if headlines, err := fetchTopHeadlines(ctx); err != nil {
		log.Printf("daily digest: headlines unavailable: %v", err)
		sections = append(sections, "📰 *Berita utama*\nBelum tersedia.")
	} else {
		sections = append(sections, formatHeadlines(headlines))
	}
	return "☀️ *Ringkasan pagi · " + date + "*\n\n" + strings.Join(sections, "\n\n──────────\n\n"), nil
}

// buildWeatherSection never fails: a city that can't be fetched is marked as
// unavailable, and if the model call fails the raw numbers are shown instead.
// It returns the forecast date, or "" when no city could be fetched.
func buildWeatherSection(ctx context.Context, cfg config) (string, string) {
	var forecasts []weatherForecast
	var missing []string
	for _, location := range jakartaAndTangerang {
		forecast, err := fetchWeatherForecast(ctx, location)
		if err != nil {
			log.Printf("daily digest: %s weather unavailable: %v", location.name, err)
			missing = append(missing, location.name)
			continue
		}
		forecasts = append(forecasts, forecast)
	}
	if len(forecasts) == 0 {
		return "🌦 *CUACA*\nCuaca belum tersedia.", ""
	}

	weatherText, err := summarizeWeather(ctx, cfg, forecasts, missing)
	if err != nil {
		log.Printf("daily digest: weather summary unavailable: %v", err)
		lines := []string{"🌦 *CUACA*"}
		for _, f := range forecasts {
			lines = append(lines, fmt.Sprintf("%s: %s, %.0f–%.0f°C, peluang hujan %.0f%%",
				f.Location, f.Condition, f.TemperatureMinC, f.TemperatureMaxC, f.PrecipitationProbability))
		}
		for _, name := range missing {
			lines = append(lines, name+": belum tersedia")
		}
		weatherText = strings.Join(lines, "\n")
	}
	return weatherText + "\nSumber: [Open-Meteo](https://open-meteo.com/en/docs)", forecasts[0].Date
}

func summarizeWeather(ctx context.Context, cfg config, forecasts []weatherForecast, missing []string) (string, error) {
	weatherJSON, err := json.Marshal(forecasts)
	if err != nil {
		return "", fmt.Errorf("encode weather forecast: %w", err)
	}
	missingNote := ""
	if len(missing) > 0 {
		missingNote = fmt.Sprintf(" The forecast for %s could not be fetched today: give it one line saying the forecast is not available, and do not guess its weather.", strings.Join(missing, " and "))
	}
	prompt := fmt.Sprintf(`Write a compact Bahasa Indonesia weather section for a friends' Telegram digest. The date in Jakarta is %s. Start with "🌦 CUACA". Give one short line for each city with condition, high/low temperature, and maximum rain probability, then one practical activity suggestion. Use only the supplied forecast values; these are daily values, so do not guess rain timing. Do not invent warnings or air-quality readings.%s Output only the weather section. Forecast JSON: %s`, forecasts[0].Date, missingNote, weatherJSON)

	weatherText, err := cfg.offlineModel().complete(ctx, []chatMessage{
		{Role: "system", Content: "You write concise, practical weather summaries from supplied data only."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return "", fmt.Errorf("write weather digest: %w", err)
	}
	return weatherText, nil
}

func summarizeExchangeWeek(ctx context.Context, cfg config, rates []exchangeRate) (string, error) {
	ratesJSON, err := json.Marshal(rates)
	if err != nil {
		return "", fmt.Errorf("encode weekly exchange rates: %w", err)
	}
	change := (rates[len(rates)-1].Rate/rates[0].Rate - 1) * 100
	prompt := fmt.Sprintf(`In one short Bahasa Indonesia sentence, summarize the observed USD/IDR movement across these dated Bank Indonesia rates. The first-to-last change is %+.2f%%. A rising USD/IDR means the rupiah weakened. Mention the direction and any visible fluctuation, but do not infer causes, add outside events, or predict future rates. Output only the sentence. Rates JSON: %s`, change, ratesJSON)
	summary, err := cfg.offlineModel().complete(ctx, []chatMessage{
		{Role: "system", Content: "Summarize only the supplied exchange-rate history. Do not speculate about causes or future prices."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(summary), " "), nil
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
