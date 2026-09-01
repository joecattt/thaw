package briefing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ClaudeConvo is one Claude Code conversation reconstructed from its
// transcript file under ~/.claude/projects/<munged-cwd>/<session-id>.jsonl.
type ClaudeConvo struct {
	ID       string
	Title    string
	Start    time.Time
	End      time.Time
	CostUSD  float64
	Duration time.Duration
}

// Ago renders how long ago the conversation last had activity.
func (c ClaudeConvo) Ago() string { return formatAgo(c.End) }

// DurationStr renders wall-clock length of the conversation.
func (c ClaudeConvo) DurationStr() string {
	m := int(c.Duration.Minutes())
	if m >= 60 {
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	if m < 1 {
		return "<1m"
	}
	return fmt.Sprintf("%dm", m)
}

// CostStr renders the estimated API-list-price cost.
func (c ClaudeConvo) CostStr() string {
	if c.CostUSD >= 10 {
		return fmt.Sprintf("$%.0f", c.CostUSD)
	}
	if c.CostUSD >= 0.01 {
		return fmt.Sprintf("$%.2f", c.CostUSD)
	}
	return "<$0.01"
}

// modelPrice is USD per million tokens at API list rates. Cache reads bill at
// 0.1x input, 5-minute cache writes at 1.25x, 1-hour cache writes at 2x.
type modelPrice struct {
	prefix  string
	in, out float64
}

// Ordered: first prefix match wins, so more specific entries come first.
var modelPrices = []modelPrice{
	{"claude-fable", 10, 50},
	{"claude-mythos", 10, 50},
	{"claude-opus-4-1", 15, 75},
	{"claude-opus-4-0", 15, 75},
	{"claude-3-opus", 15, 75},
	{"claude-opus", 5, 25},
	{"claude-sonnet", 3, 15},
	{"claude-3-5-haiku", 0.8, 4},
	{"claude-haiku", 1, 5},
}

func priceFor(model string) (in, out float64) {
	for _, p := range modelPrices {
		if strings.HasPrefix(model, p.prefix) {
			return p.in, p.out
		}
	}
	return 5, 25 // unknown model: assume Opus-tier
}

// transcriptLine holds only the fields we need from a transcript JSONL line.
type transcriptLine struct {
	Type    string `json:"type"`
	AITitle string `json:"aiTitle"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// mungeProjectDir maps a working directory to Claude Code's transcript
// directory name: every non-alphanumeric character becomes '-'.
func mungeProjectDir(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// loadClaudeConvos reads recent Claude Code transcripts for a project
// directory: newest-first conversations modified within `window`, capped at
// `max`. Returns nil when the project has no transcripts.
func loadClaudeConvos(cwd string, window time.Duration, max int) []ClaudeConvo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".claude", "projects", mungeProjectDir(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type cand struct {
		path  string
		id    string
		mtime time.Time
	}
	var cands []cand
	cutoff := time.Now().Add(-window)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		cands = append(cands, cand{
			path:  filepath.Join(dir, e.Name()),
			id:    strings.TrimSuffix(e.Name(), ".jsonl"),
			mtime: info.ModTime(),
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	if len(cands) > max {
		cands = cands[:max]
	}

	var convos []ClaudeConvo
	for _, c := range cands {
		if convo, ok := parseTranscript(c.path, c.id); ok {
			convos = append(convos, convo)
		}
	}
	return convos
}

// parseTranscript streams one transcript file, extracting the conversation
// title, first/last timestamps, and summed token usage priced per model.
func parseTranscript(path, id string) (ClaudeConvo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return ClaudeConvo{}, false
	}
	defer f.Close()

	convo := ClaudeConvo{ID: id}
	var firstTS, lastTS string
	// Active time: sum of gaps between consecutive events, with idle breaks
	// (>15min between events) excluded — honest floor, not wall-clock span.
	var active time.Duration
	var prevT time.Time
	const idleGap = 15 * time.Minute
	// One API response is written as several lines (one per content block),
	// each repeating the same usage — count each message ID once.
	seen := make(map[string]struct{})

	// Lines can be huge (inlined attachments), so read unbounded lines and
	// only JSON-decode the line types we need.
	r := bufio.NewReaderSize(f, 256*1024)
	tsKey := []byte(`"timestamp":"`)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if i := bytes.Index(line, tsKey); i >= 0 && len(line) >= i+len(tsKey)+19 {
				ts := string(line[i+len(tsKey) : i+len(tsKey)+19])
				if firstTS == "" {
					firstTS = ts
				}
				lastTS = ts
				if t, err := time.Parse("2006-01-02T15:04:05", ts); err == nil {
					if !prevT.IsZero() {
						if d := t.Sub(prevT); d > 0 && d <= idleGap {
							active += d
						}
					}
					prevT = t
				}
			}
			if bytes.Contains(line, []byte(`"type":"assistant"`)) || bytes.Contains(line, []byte(`"type":"ai-title"`)) {
				var tl transcriptLine
				if json.Unmarshal(line, &tl) == nil {
					switch tl.Type {
					case "ai-title":
						if tl.AITitle != "" {
							convo.Title = tl.AITitle
						}
					case "assistant":
						if _, dup := seen[tl.Message.ID]; dup {
							break
						}
						seen[tl.Message.ID] = struct{}{}
						u := tl.Message.Usage
						in, out := priceFor(tl.Message.Model)
						cost := float64(u.InputTokens)*in + float64(u.OutputTokens)*out +
							float64(u.CacheReadInputTokens)*in*0.1
						if w5, w1 := u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h; w5+w1 > 0 {
							cost += float64(w5)*in*1.25 + float64(w1)*in*2.0
						} else {
							cost += float64(u.CacheCreationInputTokens) * in * 1.25
						}
						convo.CostUSD += cost / 1e6
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	if firstTS == "" {
		return ClaudeConvo{}, false
	}
	// Timestamps are RFC3339 UTC; the 19-char slice "2026-08-20T07:34:13"
	// drops the fractional seconds and zone, which are always UTC.
	parse := func(s string) time.Time {
		t, err := time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	convo.Start = parse(firstTS)
	convo.End = parse(lastTS)
	if convo.Start.IsZero() || convo.End.IsZero() {
		return ClaudeConvo{}, false
	}
	convo.Duration = active
	if span := convo.End.Sub(convo.Start); convo.Duration > span {
		convo.Duration = span
	}
	if convo.Title == "" {
		convo.Title = "Untitled conversation"
	}
	return convo, true
}
