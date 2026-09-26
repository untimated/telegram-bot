package telegrambot

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var antaraTopNewsURL = "https://www.antaranews.com/rss/top-news.xml"

type headline struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type antaraFeed struct {
	Channel struct {
		Items []headline `xml:"item"`
	} `xml:"channel"`
}

func fetchTopHeadlines(ctx context.Context) ([]headline, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, antaraTopNewsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare headlines request: %w", err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch headlines: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read headlines: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch headlines: HTTP %d: %s", response.StatusCode, bodySnippet(body))
	}
	var feed antaraFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode headlines: %w", err)
	}
	var headlines []headline
	for _, item := range feed.Channel.Items {
		item.Title = strings.Join(strings.Fields(item.Title), " ")
		link, err := url.Parse(strings.TrimSpace(item.Link))
		if item.Title == "" || err != nil || link.Scheme != "https" || link.Host == "" {
			continue
		}
		item.Link = link.String()
		headlines = append(headlines, item)
		if len(headlines) == 3 {
			break
		}
	}
	if len(headlines) == 0 {
		return nil, fmt.Errorf("top news feed contained no usable headlines")
	}
	return headlines, nil
}

func formatHeadlines(headlines []headline) string {
	lines := []string{"📰 *Berita utama ANTARA*"}
	for i, item := range headlines {
		title := []rune(item.Title)
		if len(title) > 120 {
			title = append(title[:119], '…')
		}
		lines = append(lines, fmt.Sprintf("%d. %s — [Baca](%s)", i+1, string(title), item.Link))
	}
	return strings.Join(lines, "\n")
}
