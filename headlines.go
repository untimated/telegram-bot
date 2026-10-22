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

var googleNewsURL = "https://news.google.com/rss?hl=id&gl=ID&ceid=ID:id"

type headline struct {
	Title  string `xml:"title"`
	Link   string `xml:"link"`
	Source string `xml:"source"`
}

type newsFeed struct {
	Channel struct {
		Items []headline `xml:"item"`
	} `xml:"channel"`
}

func fetchTopHeadlines(ctx context.Context) ([]headline, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, googleNewsURL, nil)
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
	var feed newsFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode headlines: %w", err)
	}
	var headlines []headline
	for _, item := range feed.Channel.Items {
		item.Title = strings.Join(strings.Fields(item.Title), " ")
		item.Source = strings.TrimSpace(item.Source)
		// Google News titles end with " - Source"; the source is shown separately.
		item.Title = strings.TrimSuffix(item.Title, " - "+item.Source)
		link, err := url.Parse(strings.TrimSpace(item.Link))
		if item.Title == "" || err != nil || link.Scheme != "https" || link.Host == "" {
			continue
		}
		item.Link = link.String()
		headlines = append(headlines, item)
		if len(headlines) == 5 {
			break
		}
	}
	if len(headlines) == 0 {
		return nil, fmt.Errorf("top news feed contained no usable headlines")
	}
	return headlines, nil
}

func formatHeadlines(headlines []headline) string {
	lines := []string{"📰 *Berita utama*"}
	for i, item := range headlines {
		title := []rune(item.Title)
		if len(title) > 120 {
			title = append(title[:119], '…')
		}
		line := fmt.Sprintf("%d. %s", i+1, string(title))
		if item.Source != "" {
			line += " — " + item.Source
		}
		lines = append(lines, line+" · [Baca]("+item.Link+")")
	}
	return strings.Join(lines, "\n")
}
