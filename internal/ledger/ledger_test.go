package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// makeSnaps builds two snapshots `daysAgo` days back, 10 minutes apart, with
// one running claude session in home/dev/webapp and an idle shell in home.
func makeSnaps(home string, daysAgo int) []*models.Snapshot {
	base := time.Now().AddDate(0, 0, -daysAgo)
	sessions := []models.Session{
		{CWD: filepath.Join(home, "dev", "webapp"), Command: "/usr/local/bin/claude", Status: "running"},
		{CWD: home, Command: "zsh", Status: "running"}, // idle shell — present, never active
	}
	return []*models.Snapshot{
		{CreatedAt: base, Sessions: sessions},
		{CreatedAt: base.Add(10 * time.Minute), Sessions: sessions},
	}
}

func TestProjectOf(t *testing.T) {
	home := "/Users/x"
	cases := map[string]string{
		"":                       "~ (home ops)",
		home:                     "~ (home ops)",
		home + "/dev/webapp":     "webapp",
		home + "/dev/webapp/sub": "webapp",
		"/Users/x/Documents/foo": "foo",
		"/Users/x/notes":         "notes",
		"/tmp/scratch":           "scratch", // outside home — dir name wins
	}
	for cwd, want := range cases {
		if got := ProjectOf(cwd, home); got != want {
			t.Errorf("ProjectOf(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestBankIdempotent(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	l := New(dir)
	snaps := makeSnaps(home, 2) // recent day: unsealed, not today

	res1, err := l.Bank(snaps, home)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Rows == 0 || res1.Updated == 0 {
		t.Fatalf("first bank banked nothing: %+v", res1)
	}
	first, _ := os.ReadFile(l.ledgerPath())

	res2, err := l.Bank(snaps, home)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(l.ledgerPath())
	if string(first) != string(second) {
		t.Errorf("re-banking the same snapshots changed the ledger:\n%s\nvs\n%s", first, second)
	}
	if res2.Rows != res1.Rows {
		t.Errorf("row count drifted: %d -> %d", res1.Rows, res2.Rows)
	}
}

func TestBankNeverShrinks(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	l := New(dir)
	full := makeSnaps(home, 2)
	if _, err := l.Bank(full, home); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(l.ledgerPath())

	// Retention ate the second snapshot — a rerun sees less presence.
	if _, err := l.Bank(full[:1], home); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(l.ledgerPath())
	if string(before) != string(after) {
		t.Errorf("partial recompute shrank a banked day:\n%s\nvs\n%s", before, after)
	}
}

func TestSealAndVerify(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	l := New(dir)
	if _, err := l.Bank(makeSnaps(home, 10), home); err != nil { // old day — gets sealed
		t.Fatal(err)
	}
	res, err := l.Bank(nil, home) // second run must not double-seal
	if err != nil {
		t.Fatal(err)
	}
	if res.NewSeals != 0 || res.Seals == 0 {
		t.Fatalf("expected an already-sealed chain, got %+v", res)
	}
	problems, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("clean chain reported problems: %v", problems)
	}

	// Sealed days are immutable: a recompute of that day must not touch it.
	before, _ := os.ReadFile(l.ledgerPath())
	if _, err := l.Bank(makeSnaps(home, 10)[:1], home); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(l.ledgerPath())
	if string(before) != string(after) {
		t.Error("recompute modified a sealed day")
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	l := New(dir)
	if _, err := l.Bank(makeSnaps(home, 10), home); err != nil {
		t.Fatal(err)
	}

	// Tamper with a sealed ledger row — inflate its hours.
	data, _ := os.ReadFile(l.ledgerPath())
	tampered := strings.Replace(string(data), `"active_s":`, `"active_s":9`, 1)
	if tampered == string(data) {
		t.Fatal("tamper substitution did not apply")
	}
	os.WriteFile(l.ledgerPath(), []byte(tampered), 0600)

	problems, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("tampered ledger verified clean")
	}
	found := false
	for _, p := range problems {
		if strings.Contains(p, "TAMPERED") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a TAMPERED finding, got %v", problems)
	}
}

func TestVerifyDetectsChainEdit(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	l := New(dir)
	if _, err := l.Bank(makeSnaps(home, 10), home); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(l.chainPath())
	edited := strings.Replace(string(data), `"rows":`, `"rows":1`, 1)
	if edited == string(data) {
		t.Fatal("chain edit did not apply")
	}
	os.WriteFile(l.chainPath(), []byte(edited), 0600)

	problems, err := l.Verify()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range problems {
		if strings.Contains(p, "BROKEN CHAIN") {
			found = true
		}
	}
	if !found {
		t.Errorf("edited chain did not report BROKEN CHAIN: %v", problems)
	}
}

// TestCanonicalRowMatchesPython pins the digest encoding to Python's
// json.dumps(sort_keys=True) — the format the operator's existing sealed
// chains were built with. Expected string produced by CPython 3.x:
//
//	json.dumps({"d":"2026-07-28","p":"webapp","active_s":8100,
//	            "present_s":9900,"cmds":{"claude":14,"go test":2}}, sort_keys=True)
func TestCanonicalRowMatchesPython(t *testing.T) {
	r := Row{Day: "2026-07-28", Project: "webapp", ActiveS: 8100, PresentS: 9900,
		Cmds: map[string]int{"claude": 14, "go test": 2}}
	want := `{"active_s": 8100, "cmds": {"claude": 14, "go test": 2}, "d": "2026-07-28", "p": "webapp", "present_s": 9900}`
	if got := canonicalRow(r); got != want {
		t.Errorf("canonicalRow mismatch:\n got %s\nwant %s", got, want)
	}
	sum := sha256.Sum256([]byte(want))
	if got := DayDigest([]Row{r}); got != hex.EncodeToString(sum[:]) {
		t.Errorf("DayDigest mismatch for single row")
	}
	// ensure_ascii + escapes: non-ASCII becomes \uXXXX, quotes/newlines escaped
	if got := pyStr("café \"x\"\n"); got != `"caf\u00e9 \"x\"\n"` {
		t.Errorf("pyStr mismatch: %s", got)
	}
}
