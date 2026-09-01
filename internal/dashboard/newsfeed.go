package dashboard

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// NewsItem is one headline for the news rail — small, stacked cards, not a
// rotating single slot: "small windows above each other," per spec.
type NewsItem struct {
	Title  string
	Source string
	URL    string
	Image  string // empty when the source has no thumbnail — card renders text-only, not broken
}

var newsHTTPClient = &http.Client{Timeout: 4 * time.Second}

// NewsSource is one entry in the registry FetchNews reads from. Free/no-key
// only — anything needing an API key is a bigger commitment (credential
// storage, the exact class of risk this session spent an hour cleaning up
// elsewhere) and doesn't belong in a config toggle.
type NewsSource struct {
	Name  string
	Fetch func(n int) []NewsItem
}

// NewsSources is the registry `[news] sources = [...]` in config.toml picks
// from by key. Add a new free/no-key RSS source by adding one line here —
// no other code changes needed.
var NewsSources = map[string]NewsSource{
	"hn":         {Name: "Hacker News", Fetch: fetchHN},
	"bbc":        {Name: "BBC World", Fetch: func(n int) []NewsItem { return fetchRSS("https://feeds.bbci.co.uk/news/world/rss.xml", "BBC World", n) }},
	"scotusblog": {Name: "SCOTUSblog", Fetch: func(n int) []NewsItem { return fetchRSS("https://www.scotusblog.com/feed/", "SCOTUSblog", n) }},
	"gizmodo":    {Name: "Gizmodo", Fetch: func(n int) []NewsItem { return fetchRSS("https://gizmodo.com/feed", "Gizmodo", n) }},
	"techcrunch": {Name: "TechCrunch", Fetch: func(n int) []NewsItem { return fetchRSS("https://techcrunch.com/feed/", "TechCrunch", n) }},
}

// FetchNews pulls a handful of items from each configured source (see
// NewsSources) for the left news rail. Best-effort throughout: this is a
// static file generated offline most of the time, so any source being
// unreachable just means fewer cards, never a failed dashboard. Runs every
// source concurrently so one slow one doesn't stall the rest. Unknown keys
// in the config are silently skipped, not an error — a typo'd source name
// shouldn't break the whole dashboard. Deduped by normalized title — the
// same story routinely gets picked up by more than one feed, and operator
// feedback (2026-08-29) was specifically "too many repeating stories."
func FetchNews(sourceKeys []string, perSource int) []NewsItem {
	type result struct {
		items []NewsItem
	}
	ch := make(chan result, len(sourceKeys))
	fired := 0
	for _, key := range sourceKeys {
		src, ok := NewsSources[key]
		if !ok {
			continue
		}
		fired++
		go func(f func(int) []NewsItem) { ch <- result{f(perSource)} }(src.Fetch)
	}

	var all []NewsItem
	for i := 0; i < fired; i++ {
		r := <-ch
		all = append(all, r.items...)
	}
	return dedupNews(all)
}

func dedupNews(items []NewsItem) []NewsItem {
	seen := map[string]bool{}
	var out []NewsItem
	for _, it := range items {
		key := strings.ToLower(wsRe.ReplaceAllString(it.Title, " "))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, it)
	}
	return out
}

func fetchHN(n int) []NewsItem {
	resp, err := newsHTTPClient.Get("https://hacker-news.firebaseio.com/v0/topstories.json")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var ids []int
	if json.NewDecoder(resp.Body).Decode(&ids) != nil || len(ids) == 0 {
		return nil
	}
	if len(ids) > n {
		ids = ids[:n]
	}
	var items []NewsItem
	for _, id := range ids {
		r2, err := newsHTTPClient.Get(fmt.Sprintf("https://hacker-news.firebaseio.com/v0/item/%d.json", id))
		if err != nil {
			continue
		}
		var it struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		decErr := json.NewDecoder(r2.Body).Decode(&it)
		r2.Body.Close()
		if decErr != nil || it.Title == "" {
			continue
		}
		url := it.URL
		if url == "" {
			url = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", id)
		}
		items = append(items, NewsItem{Title: it.Title, Source: "Hacker News", URL: url})
	}
	return items
}

// rssXML is a minimal RSS 2.0 shape — just enough to pull title/link/image
// out of the handful of feeds this rail uses. Thumbnail covers BBC's native
// <media:thumbnail url="...">; ContentEncoded is a fallback source to
// regex an <img> out of for feeds (SCOTUSblog) that don't tag one directly —
// Go's xml decoder matches by local name regardless of namespace prefix, so
// this doesn't need the media/content namespace URIs declared.
type rssXML struct {
	Channel struct {
		Items []struct {
			Title     string `xml:"title"`
			Link      string `xml:"link"`
			Thumbnail struct {
				URL string `xml:"url,attr"`
			} `xml:"thumbnail"`
			Description    string `xml:"description"`
			ContentEncoded string `xml:"encoded"`
		} `xml:"item"`
	} `xml:"channel"`
}

var (
	wsRe     = regexp.MustCompile(`\s+`)
	imgSrcRe = regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
)

func firstImg(sources ...string) string {
	for _, s := range sources {
		if m := imgSrcRe.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return ""
}

func fetchRSS(url, sourceName string, n int) []NewsItem {
	resp, err := newsHTTPClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MB cap — content:encoded bodies push past the old 2MB
	if err != nil {
		return nil
	}
	var feed rssXML
	if xml.Unmarshal(body, &feed) != nil {
		return nil
	}
	var items []NewsItem
	for _, it := range feed.Channel.Items {
		title := strings.TrimSpace(wsRe.ReplaceAllString(it.Title, " "))
		if title == "" {
			continue
		}
		img := it.Thumbnail.URL
		if img == "" {
			img = firstImg(it.ContentEncoded, it.Description)
		}
		items = append(items, NewsItem{Title: title, Source: sourceName, URL: strings.TrimSpace(it.Link), Image: img})
		if len(items) >= n {
			break
		}
	}
	return items
}
