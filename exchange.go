package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var frankfurterRateURL = "https://api.frankfurter.dev/v2/rate/USD/IDR?providers=BI"
var frankfurterHistoryURL = "https://api.frankfurter.dev/v2/rates"

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

// fetchUSDIDRWeek returns published business-day rates in the seven calendar
// days ending with the latest rate. Weekend and holiday gaps are expected.
func fetchUSDIDRWeek(ctx context.Context, latestDate string) ([]exchangeRate, error) {
	end, err := time.Parse("2006-01-02", latestDate)
	if err != nil {
		return nil, fmt.Errorf("invalid latest exchange date %q: %w", latestDate, err)
	}
	endpoint, err := url.Parse(frankfurterHistoryURL)
	if err != nil {
		return nil, fmt.Errorf("prepare weekly exchange request: %w", err)
	}
	query := endpoint.Query()
	query.Set("base", "USD")
	query.Set("quotes", "IDR")
	query.Set("providers", "BI")
	query.Set("from", end.AddDate(0, 0, -6).Format("2006-01-02"))
	query.Set("to", latestDate)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prepare weekly exchange request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch weekly exchange rates: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read weekly exchange rates: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch weekly exchange rates: HTTP %d: %s", response.StatusCode, bodySnippet(body))
	}
	var rates []exchangeRate
	if err := json.Unmarshal(body, &rates); err != nil {
		return nil, fmt.Errorf("decode weekly exchange rates: %w", err)
	}
	valid := rates[:0]
	for _, rate := range rates {
		date, err := time.Parse("2006-01-02", rate.Date)
		if err != nil || date.Before(end.AddDate(0, 0, -6)) || date.After(end) ||
			rate.Base != "USD" || rate.Quote != "IDR" || rate.Rate <= 0 || math.IsNaN(rate.Rate) || math.IsInf(rate.Rate, 0) {
			continue
		}
		valid = append(valid, rate)
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Date < valid[j].Date })
	if len(valid) < 2 || valid[len(valid)-1].Date != latestDate {
		return nil, fmt.Errorf("weekly exchange rates do not contain two dates ending on %s", latestDate)
	}
	return valid, nil
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
