package dashboard

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/joecattt/thaw/internal/briefing"
)

// LedgerRow mirrors one line of ~/.local/share/thaw/ledger.jsonl — thaw-ledger's
// permanent, append-only, honest-time store (presence-derived, gaps capped
// 45min, idle shells excluded; never pruned, unlike the raw snapshot store
// this dashboard used to compute its EstHours guess from).
type LedgerRow struct {
	Date     string `json:"d"`
	Project  string `json:"p"`
	ActiveS  int64  `json:"active_s"`
	PresentS int64  `json:"present_s"`
}

// LedgerHistory reads the permanent ledger and buckets active seconds by day
// and by project, for everything on or after `since`. This is the real,
// long-horizon time record — independent of the 7-day snapshot retention
// that the rest of the dashboard's per-project git data is bounded by.
func LedgerHistory(since time.Time) (byDay map[string]int64, byProject map[string]int64, err error) {
	path := filepath.Join(os.Getenv("HOME"), ".local", "share", "thaw", "ledger.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	byDay = map[string]int64{}
	byProject = map[string]int64{}
	cutoff := since.Format("2006-01-02")

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var row LedgerRow
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		if row.Date < cutoff {
			continue
		}
		byDay[row.Date] += row.ActiveS
		byProject[row.Project] += row.ActiveS
	}
	return byDay, byProject, sc.Err()
}

// AISpendHistory scans real Claude Code transcript cost data (token-priced,
// cache-aware, deduped by message ID — the same engine the resume briefing
// card uses) for every project root already discovered by Collect, and
// buckets it by day. Best-effort: a project with no transcripts just
// contributes nothing, not an error.
func AISpendHistory(projectRoots []string, since time.Time) (byDay map[string]float64, byProject map[string]float64) {
	byDay = map[string]float64{}
	byProject = map[string]float64{}
	for _, root := range projectRoots {
		convos := briefing.ConvosForProject(root, since)
		for _, c := range convos {
			d := c.End.Format("2006-01-02")
			byDay[d] += c.CostUSD
			byProject[root] += c.CostUSD
		}
	}
	return byDay, byProject
}
