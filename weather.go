package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const openMeteoBaseURL = "https://api.open-meteo.com"

type weatherLocation struct {
	name      string
	latitude  string
	longitude string
}

type weatherForecast struct {
	Location                 string  `json:"location"`
	Date                     string  `json:"date"`
	Condition                string  `json:"condition"`
	TemperatureMaxC          float64 `json:"temperature_max_c"`
	TemperatureMinC          float64 `json:"temperature_min_c"`
	ApparentTemperatureMaxC  float64 `json:"apparent_temperature_max_c"`
	PrecipitationProbability float64 `json:"precipitation_probability_percent"`
	PrecipitationSumMM       float64 `json:"precipitation_sum_mm"`
	WindSpeedMaxKmh          float64 `json:"wind_speed_max_kmh"`
}

type openMeteoResponse struct {
	Daily struct {
		Time                     []string  `json:"time"`
		WeatherCode              []int     `json:"weather_code"`
		TemperatureMax           []float64 `json:"temperature_2m_max"`
		TemperatureMin           []float64 `json:"temperature_2m_min"`
		ApparentTemperatureMax   []float64 `json:"apparent_temperature_max"`
		PrecipitationProbability []float64 `json:"precipitation_probability_max"`
		PrecipitationSum         []float64 `json:"precipitation_sum"`
		WindSpeedMax             []float64 `json:"wind_speed_10m_max"`
	} `json:"daily"`
}

func fetchWeatherForecast(ctx context.Context, location weatherLocation) (weatherForecast, error) {
	query := url.Values{}
	query.Set("latitude", location.latitude)
	query.Set("longitude", location.longitude)
	query.Set("daily", strings.Join([]string{
		"weather_code",
		"temperature_2m_max",
		"temperature_2m_min",
		"apparent_temperature_max",
		"precipitation_probability_max",
		"precipitation_sum",
		"wind_speed_10m_max",
	}, ","))
	query.Set("temperature_unit", "celsius")
	query.Set("wind_speed_unit", "kmh")
	query.Set("precipitation_unit", "mm")
	query.Set("timezone", "Asia/Jakarta")
	query.Set("forecast_days", "1")

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		openMeteoBaseURL+"/v1/forecast?"+query.Encode(), nil)
	if err != nil {
		return weatherForecast{}, fmt.Errorf("prepare %s forecast request: %w", location.name, err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return weatherForecast{}, fmt.Errorf("fetch %s forecast: %w", location.name, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return weatherForecast{}, fmt.Errorf("read %s forecast: %w", location.name, err)
	}
	if response.StatusCode != http.StatusOK {
		return weatherForecast{}, fmt.Errorf("fetch %s forecast: HTTP %d: %s",
			location.name, response.StatusCode, bodySnippet(body))
	}

	var data openMeteoResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return weatherForecast{}, fmt.Errorf("decode %s forecast: %w", location.name, err)
	}
	daily := data.Daily
	if len(daily.Time) == 0 || len(daily.WeatherCode) == 0 || len(daily.TemperatureMax) == 0 ||
		len(daily.TemperatureMin) == 0 || len(daily.ApparentTemperatureMax) == 0 ||
		len(daily.PrecipitationProbability) == 0 || len(daily.PrecipitationSum) == 0 ||
		len(daily.WindSpeedMax) == 0 {
		return weatherForecast{}, fmt.Errorf("decode %s forecast: response is missing daily values", location.name)
	}

	return weatherForecast{
		Location:                 location.name,
		Date:                     daily.Time[0],
		Condition:                weatherCondition(daily.WeatherCode[0]),
		TemperatureMaxC:          daily.TemperatureMax[0],
		TemperatureMinC:          daily.TemperatureMin[0],
		ApparentTemperatureMaxC:  daily.ApparentTemperatureMax[0],
		PrecipitationProbability: daily.PrecipitationProbability[0],
		PrecipitationSumMM:       daily.PrecipitationSum[0],
		WindSpeedMaxKmh:          daily.WindSpeedMax[0],
	}, nil
}

func weatherCondition(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1:
		return "mainly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75, 77:
		return "snow"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "unclassified weather"
	}
}
