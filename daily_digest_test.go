package telegrambot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setupDigestSources(t *testing.T, failExtras bool) {
	t.Helper()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/forecast":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"daily":{"time":["2026-09-26"],"weather_code":[3],"temperature_2m_max":[32],"temperature_2m_min":[25],"apparent_temperature_max":[36],"precipitation_probability_max":[70],"precipitation_sum":[2],"wind_speed_10m_max":[12]}}`)
		case "/v2/rate/USD/IDR":
			if failExtras {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"date":"2026-09-25","base":"USD","quote":"IDR","rate":16512.5}`)
		case "/v2/rates":
			if failExtras {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("base") != "USD" || r.URL.Query().Get("quotes") != "IDR" ||
				r.URL.Query().Get("providers") != "BI" || r.URL.Query().Get("from") != "2026-09-19" ||
				r.URL.Query().Get("to") != "2026-09-25" {
				http.Error(w, "wrong weekly range", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `[{"date":"2026-09-25","base":"USD","quote":"IDR","rate":16512.5},{"date":"2026-09-22","base":"USD","quote":"IDR","rate":16400}]`)
		case "/rss/top-news.xml":
			if failExtras {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `<rss><channel><item><title>Headline one</title><link>https://www.antaranews.com/one</link></item><item><title>Headline two</title><link>https://www.antaranews.com/two</link></item><item><title>Headline three</title><link>https://www.antaranews.com/three</link></item></channel></rss>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(source.Close)
	oldWeather, oldRate, oldHistory, oldNews := openMeteoBaseURL, frankfurterRateURL, frankfurterHistoryURL, antaraTopNewsURL
	openMeteoBaseURL = source.URL
	frankfurterRateURL = source.URL + "/v2/rate/USD/IDR?providers=BI"
	frankfurterHistoryURL = source.URL + "/v2/rates"
	antaraTopNewsURL = source.URL + "/rss/top-news.xml"
	t.Cleanup(func() {
		openMeteoBaseURL, frankfurterRateURL, frankfurterHistoryURL, antaraTopNewsURL = oldWeather, oldRate, oldHistory, oldNews
	})
}

func TestGroupDigestCommandUsesAllThreeSources(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.responses = []map[string]any{
		textCompletion("🌦 CUACA\nJakarta dan Tangerang berawan. Bawa payung."),
		textCompletion("Dalam sepekan, USD/IDR naik dengan sedikit fluktuasi."),
		textCompletion("🌦 CUACA\nJakarta dan Tangerang berawan. Bawa payung."),
		textCompletion("Dalam sepekan, USD/IDR naik dengan sedikit fluktuasi."),
	}
	setupBot(t, fake)
	setupDigestSources(t, false)
	t.Setenv("ALLOWED_CHAT_IDS", "-100")

	if code := postUpdate(t, groupUpdate("/digest", false), "").Code; code != http.StatusOK {
		t.Fatalf("webhook status = %d", code)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].ChatID != -100 {
		t.Fatalf("sent = %v, want one group digest", sent)
	}
	for _, want := range []string{"Ringkasan pagi", "Jakarta dan Tangerang", "16.512", "2026-09-25", "2026-09-22–2026-09-25", "+0.69%", "sedikit fluktuasi", "Headline one", "Headline two", "Headline three", "──────────"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("digest %q is missing %q", sent[0].Text, want)
		}
	}
	if len(fake.deepSeekRequests()) != 2 {
		t.Errorf("model calls = %d, want one for weather and one for the weekly trend", len(fake.deepSeekRequests()))
	} else {
		prompt := lastUserMessage(t, fake.deepSeekRequests()[1])["content"].(string)
		if !strings.Contains(prompt, "2026-09-22") || !strings.Contains(prompt, "2026-09-25") || !strings.Contains(prompt, "do not infer causes") {
			t.Errorf("weekly trend prompt lacks rates or guardrails: %s", prompt)
		}
	}
	postUpdate(t, groupUpdate("/digest@otherbot", false), "")
	if len(fake.sentMessages()) != 1 {
		t.Error("command for another bot caused a reply")
	}
	postUpdate(t, groupUpdate("/digest@TestBot", false), "")
	if len(fake.sentMessages()) != 2 {
		t.Error("command addressed to this bot did not produce a digest")
	}
}

func TestDigestCommandRespectsChatAllowList(t *testing.T) {
	fake := newFakeUpstream(t)
	setupBot(t, fake)
	t.Setenv("ALLOWED_CHAT_IDS", "4242")
	postUpdate(t, groupUpdate("/digest", false), "")
	if fake.requestCount() != 0 {
		t.Errorf("blocked group made %d upstream calls", fake.requestCount())
	}
}

func TestScheduledDigestSurvivesMissingFinanceAndNews(t *testing.T) {
	fake := newFakeUpstream(t)
	fake.reply = "🌦 CUACA\nBawa payung."
	setupBot(t, fake)
	setupDigestSources(t, true)
	t.Setenv("DAILY_CHAT_ID", "-100")
	t.Setenv("ALLOWED_CHAT_IDS", "-100")

	if err := RunDailyWeatherDigest(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent = %v, want one weather digest", sent)
	}
	for _, want := range []string{"Bawa payung", "Kurs belum tersedia", "Belum tersedia"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("digest is missing %q: %s", want, sent[0].Text)
		}
	}
}
