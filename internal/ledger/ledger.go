// Package ledger banks each day's per-project terminal time into a permanent
// append-only ledger BEFORE retention deletes the raw snapshots, and seals
// finalized days into a sha256 hash chain so history can't be silently
// rewritten. Ported from the operator's ~/bin/thaw-ledger satellite script;
// the canonical row encoding deliberately mirrors Python's
// json.dumps(sort_keys=True) so chains sealed by the script still verify.
//
// Ledger: <data-dir>/ledger.jsonl — one line per (day, project):
//
//	{"d":"2026-07-28","p":"webapp","active_s":8100,"present_s":9900,"cmds":{"claude":14}}
//
// Time model (honest): each snapshot proves which sessions existed at that
// moment; the gap to the previous snapshot (capped at 45 min) is credited to
// every project present. "active" = a session actually running something
// (not an idle shell). Presence-derived time, not keystroke tracking.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

const (
	gapCapSeconds = 45 * 60 // max credit between snapshots — laptop was closed
	minPresence   = 300     // under 5 min presence that day: noise, don't bank
	sealAfterDays = 8       // seal days older than retention — raw data is gone
)

// Row is one banked (day, project) line in ledger.jsonl.
type Row struct {
	Day      string         `json:"d"`
	Project  string         `json:"p"`
	ActiveS  int            `json:"active_s"`
	PresentS int            `json:"present_s"`
	Cmds     map[string]int `json:"cmds"`
}

// ChainEntry is one line in ledger-chain.jsonl — either a seal (Digest set)
// or a correction superseding an earlier seal (Correction true).
type ChainEntry struct {
	Correction bool   `json:"correction,omitempty"`
	Day        string `json:"d"`
	Rows       int    `json:"rows,omitempty"`
	Digest     string `json:"digest,omitempty"`
	OldDigest  string `json:"old_digest,omitempty"`
	NewDigest  string `json:"new_digest,omitempty"`
	Reason     string `json:"reason,omitempty"`
	At         string `json:"at,omitempty"`
	Prev       string `json:"prev"`
	Hash       string `json:"h"`
}

// Ledger operates on the ledger + chain files in Dir.
type Ledger struct {
	Dir string
}

func New(dir string) *Ledger { return &Ledger{Dir: dir} }

func (l *Ledger) ledgerPath() string { return filepath.Join(l.Dir, "ledger.jsonl") }
func (l *Ledger) chainPath() string  { return filepath.Join(l.Dir, "ledger-chain.jsonl") }

// ProjectOf maps a working directory to a project name — the repo/dir name.
// Home itself is its own bucket; well-known container dirs (dev, Documents,
// Desktop) are skipped so the project under them gets the identity.
func ProjectOf(cwd, home string) string {
	if cwd == "" || cwd == home {
		return "~ (home ops)"
	}
	rel, err := filepath.Rel(home, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(cwd) // outside home — the dir name is the project
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) > 1 && (parts[0] == "dev" || parts[0] == "Documents" || parts[0] == "Desktop") {
		return parts[1]
	}
	return parts[0]
}

// Rollup aggregates snapshots into day → project → Row (Day/Project unset —
// filled in at bank time). Snapshots must be in chronological order.
func Rollup(snaps []*models.Snapshot, home string) map[string]map[string]*Row {
	days := map[string]map[string]*Row{}
	var prev time.Time
	for _, snap := range snaps {
		t := snap.CreatedAt
		gap := gapCapSeconds
		if !prev.IsZero() {
			g := int(t.Sub(prev).Seconds())
			if g < 0 {
				g = 0
			}
			if g < gap {
				gap = g
			}
		}
		prev = t
		day := t.Format("2006-01-02")
		if days[day] == nil {
			days[day] = map[string]*Row{}
		}
		present := map[string]bool{}
		active := map[string]bool{}
		for _, s := range snap.Sessions {
			p := ProjectOf(s.CWD, home)
			present[p] = true
			// last path component of the foreground command, truncated
			cmd := s.Command
			if i := strings.LastIndex(cmd, "/"); i >= 0 {
				cmd = cmd[i+1:]
			}
			if len(cmd) > 24 {
				cmd = cmd[:24]
			}
			if strings.EqualFold(s.Status, "running") && !isIdleCmd(cmd) {
				active[p] = true
				row := ensureRow(days[day], p)
				row.Cmds[cmd]++
			}
		}
		for p := range present {
			ensureRow(days[day], p).PresentS += gap
		}
		for p := range active {
			ensureRow(days[day], p).ActiveS += gap
		}
	}
	return days
}

func isIdleCmd(cmd string) bool {
	switch cmd {
	case "zsh", "bash", "-zsh", "sleep", "(sleep)":
		return true
	}
	return false
}

func ensureRow(m map[string]*Row, p string) *Row {
	if m[p] == nil {
		m[p] = &Row{Cmds: map[string]int{}}
	}
	return m[p]
}

// BankResult reports what a banking run did.
type BankResult struct {
	Rows     int // total rows in the ledger after banking
	Updated  int // rows added or updated this run
	NewSeals int // chain entries appended this run
	Seals    int // total sealed days in the chain
}

// Bank merges a fresh rollup of snaps into the ledger, then seals finalized
// days. Idempotent: re-running a day overwrites that day's lines, never
// duplicates. Guards, in order of authority:
//   - a SEALED day is immutable, full stop — no recompute touches it
//   - an unsealed banked day never shrinks (raw data for old days may be
//     partial once retention has eaten some snapshots — keep the larger record)
//   - today is always overwritable (it's still accumulating)
func (l *Ledger) Bank(snaps []*models.Snapshot, home string) (BankResult, error) {
	var res BankResult
	if err := os.MkdirAll(l.Dir, 0700); err != nil {
		return res, err
	}
	old, err := l.loadRows()
	if err != nil {
		return res, err
	}
	chain, err := l.loadChain()
	if err != nil {
		return res, err
	}
	sealedDays := map[string]bool{}
	for _, e := range chain {
		sealedDays[e.Day] = true
	}
	fresh := Rollup(snaps, home)
	today := time.Now().Format("2006-01-02")

	type key struct{ d, p string }
	byKey := map[key]Row{}
	for _, r := range old {
		byKey[key{r.Day, r.Project}] = r
	}
	// Orphan cleanup (unsealed days only): a project that used to bucket one
	// way and now buckets another leaves a stale row behind — drop any row
	// whose day IS freshly recomputed but whose project ISN'T in the recompute.
	for k := range byKey {
		if sealedDays[k.d] || fresh[k.d] == nil {
			continue
		}
		if fresh[k.d][k.p] == nil {
			delete(byKey, k)
		}
	}
	for day, projs := range fresh {
		if sealedDays[day] { // sealed = immutable, full stop
			continue
		}
		for p, e := range projs {
			if e.PresentS < minPresence {
				continue
			}
			rec := Row{Day: day, Project: p, ActiveS: e.ActiveS, PresentS: e.PresentS,
				Cmds: topCmds(e.Cmds, 6)}
			k := key{day, p}
			if prev, ok := byKey[k]; ok && day != today && prev.PresentS >= rec.PresentS {
				continue // never shrink a banked day
			}
			byKey[k] = rec
			res.Updated++
		}
	}
	rows := make([]Row, 0, len(byKey))
	for _, r := range byKey {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Day != rows[j].Day {
			return rows[i].Day < rows[j].Day
		}
		return rows[i].Project < rows[j].Project
	})
	if err := l.writeRows(rows); err != nil {
		return res, err
	}
	res.Rows = len(rows)
	res.NewSeals, res.Seals, err = l.seal(rows, chain, sealedDays)
	return res, err
}

// topCmds keeps the n most-frequent commands (ties broken by name for
// determinism — banking must be byte-stable across reruns).
func topCmds(cmds map[string]int, n int) map[string]int {
	if len(cmds) <= n {
		return cmds
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(cmds))
	for k, v := range cmds {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	out := map[string]int{}
	for _, e := range all[:n] {
		out[e.k] = e.v
	}
	return out
}

// seal appends hash-chain entries for finalized days (older than snapshot
// retention — their raw snapshots are gone, so the row can never legitimately
// change). Chain is append-only; each entry commits to the previous one, so
// silently editing an old day breaks every later hash.
func (l *Ledger) seal(rows []Row, chain []ChainEntry, sealedDays map[string]bool) (newSeals, total int, err error) {
	prev := "genesis"
	if len(chain) > 0 {
		prev = chain[len(chain)-1].Hash
	}
	cutoff := time.Now().AddDate(0, 0, -sealAfterDays).Format("2006-01-02")
	byDay := map[string][]Row{}
	for _, r := range rows {
		byDay[r.Day] = append(byDay[r.Day], r)
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	f, err := os.OpenFile(l.chainPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 0, len(chain), err
	}
	defer f.Close()
	for _, day := range days {
		if day > cutoff || sealedDays[day] {
			continue
		}
		e := ChainEntry{Day: day, Rows: len(byDay[day]), Digest: DayDigest(byDay[day]), Prev: prev}
		e.Hash = entryHash(e)
		line, _ := json.Marshal(e)
		if _, err := f.Write(append(line, '\n')); err != nil {
			return newSeals, len(chain) + newSeals, err
		}
		prev = e.Hash
		newSeals++
	}
	return newSeals, len(chain) + newSeals, nil
}

// DayDigest is the canonical digest of one day's ledger rows (sorted by
// project). Encoding matches Python json.dumps(sort_keys=True) byte-for-byte
// so seals written by the original script still verify.
func DayDigest(rows []Row) string {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Project < sorted[j].Project })
	lines := make([]string, len(sorted))
	for i, r := range sorted {
		lines[i] = canonicalRow(r)
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// canonicalRow renders a row exactly as json.dumps(row, sort_keys=True) would:
// keys alphabetical, ", "/": " separators, non-ASCII escaped as \uXXXX.
func canonicalRow(r Row) string {
	var b strings.Builder
	b.WriteString(`{"active_s": `)
	fmt.Fprintf(&b, "%d", r.ActiveS)
	b.WriteString(`, "cmds": {`)
	keys := make([]string, 0, len(r.Cmds))
	for k := range r.Cmds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pyStr(k))
		b.WriteString(": ")
		fmt.Fprintf(&b, "%d", r.Cmds[k])
	}
	b.WriteString(`}, "d": `)
	b.WriteString(pyStr(r.Day))
	b.WriteString(`, "p": `)
	b.WriteString(pyStr(r.Project))
	b.WriteString(`, "present_s": `)
	fmt.Fprintf(&b, "%d", r.PresentS)
	b.WriteString("}")
	return b.String()
}

// pyStr renders a JSON string the way Python json.dumps does (ensure_ascii).
func pyStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(&b, `\u%04x`, r)
			case r < 0x7f:
				b.WriteRune(r)
			case r > 0xffff: // astral plane → surrogate pair, like Python
				v := r - 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(v>>10), 0xdc00+(v&0x3ff))
			default:
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func entryHash(e ChainEntry) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", e.Prev, e.Day, e.Rows, e.Digest)))
	return hex.EncodeToString(sum[:])
}

func correctionHash(e ChainEntry) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|CORRECTION|%s|%s|%s|%s",
		e.Prev, e.Day, e.OldDigest, e.NewDigest, e.Reason)))
	return hex.EncodeToString(sum[:])
}

// Verify checks chain integrity AND that sealed days still match the ledger.
// A day whose seal was superseded by a CORRECTION entry verifies against the
// corrected digest (and is reported as corrected, never hidden). Returns the
// problem list — empty means the chain holds.
func (l *Ledger) Verify() ([]string, error) {
	chain, err := l.loadChain()
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, nil // nothing sealed yet
	}
	rows, err := l.loadRows()
	if err != nil {
		return nil, err
	}
	byDay := map[string][]Row{}
	for _, r := range rows {
		byDay[r.Day] = append(byDay[r.Day], r)
	}
	// effective digest per day = last non-superseded word in the chain
	effective := map[string]string{}
	var problems []string
	prev := "genesis"
	for i, e := range chain {
		if e.Correction {
			if e.Prev != prev || correctionHash(e) != e.Hash {
				problems = append(problems, fmt.Sprintf("BROKEN CHAIN at correction entry %d (day %s)", i, e.Day))
			}
			if effective[e.Day] != e.OldDigest {
				problems = append(problems, fmt.Sprintf("BAD CORRECTION at entry %d: old_digest doesn't match the seal it claims to supersede", i))
			}
			effective[e.Day] = e.NewDigest
			prev = e.Hash
			continue
		}
		if e.Prev != prev || entryHash(e) != e.Hash {
			problems = append(problems, fmt.Sprintf("BROKEN CHAIN at entry %d (day %s) — chain file was edited or reordered", i, e.Day))
		}
		prev = e.Hash
		effective[e.Day] = e.Digest
	}
	for day, dig := range effective {
		if byDay[day] == nil {
			problems = append(problems, fmt.Sprintf("MISSING: day %s sealed but absent from ledger", day))
		} else if DayDigest(byDay[day]) != dig {
			problems = append(problems, fmt.Sprintf("TAMPERED: day %s ledger rows differ from sealed digest", day))
		}
	}
	return problems, nil
}

// SealedDays returns how many days the chain currently seals.
func (l *Ledger) SealedDays() (int, error) {
	chain, err := l.loadChain()
	if err != nil {
		return 0, err
	}
	days := map[string]bool{}
	for _, e := range chain {
		days[e.Day] = true
	}
	return len(days), nil
}

func (l *Ledger) loadRows() ([]Row, error) {
	data, err := os.ReadFile(l.ledgerPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rows []Row
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // malformed line — skip, same as the script
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (l *Ledger) writeRows(rows []Row) error {
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp := l.ledgerPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, l.ledgerPath())
}

func (l *Ledger) loadChain() ([]ChainEntry, error) {
	data, err := os.ReadFile(l.chainPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []ChainEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e ChainEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}
