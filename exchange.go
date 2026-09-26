package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

var frankfurterRateURL = "https://api.frankfurter.dev/v2/rate/USD/IDR?providers=BI"

type exchangeRate struct {
	Date  string  `json:"date"`
	Base  string  `json:"base"`
	Quote string  `json:"quote"`
	Rate  float64 `json:"rate"`
}

func fetchUSDtoIDR(ctx context.Context) (exchangeRate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, frankfurterRateURL, nil)
	if err != nil {
		return exchangeRate{}, fmt.Errorf("prepare exchange rate request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return exchangeRate{}, fmt.Errorf("fetch exchange rate: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return exchangeRate{}, fmt.Errorf("read exchange rate: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return exchangeRate{}, fmt.Errorf("fetch exchange rate: HTTP %d: %s", response.StatusCode, bodySnippet(body))
	}
	var rate exchangeRate
	if err := json.Unmarshal(body, &rate); err != nil {
		return exchangeRate{}, fmt.Errorf("decode exchange rate: %w", err)
	}
	if rate.Base != "USD" || rate.Quote != "IDR" || rate.Rate <= 0 || math.IsInf(rate.Rate, 0) || math.IsNaN(rate.Rate) {
		return exchangeRate{}, fmt.Errorf("exchange rate response is missing a valid USD/IDR value")
	}
	if _, err := time.Parse("2006-01-02", rate.Date); err != nil {
		return exchangeRate{}, fmt.Errorf("exchange rate response has invalid date %q", rate.Date)
	}
	return rate, nil
}

func formatExchangeRate(rate exchangeRate) string {
	whole := fmt.Sprintf("%.0f", rate.Rate)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "." + whole[i:]
	}
	return strings.Join([]string{
		"💱 *USD/IDR*",
		"USD 1 ≈ Rp " + whole,
		"Kurs " + rate.Date + " · [Bank Indonesia via Frankfurter](https://frankfurter.dev/providers/bi/)",
	}, "\n")
}
