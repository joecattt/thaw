package recap

import (
	"testing"
)

func testConfig() EffortConfig {
	return DefaultEffortConfig()
}

func TestClassifyCommit(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		subject string
		want    string
	}{
		{"feat: add login page", "feat"},
		{"feat(api): add endpoint", "feat"},
		{"feat!: breaking change", "feat"},
		{"fix: nil pointer in store", "fix"},
		{"refactor: split snapshot package", "refactor"},
		{"perf: speed up diff", "perf"},
		{"test: add table-driven cases", "test"},
		{"docs: update README", "docs"},
		{"chore: bump deps", "chore"},
		{"style: gofmt", "style"},
		{"security: patch path traversal", "security"},
		{"Feat: capitalized prefix", "feat"},
		{"FIX(scope): upper case", "fix"},
		// keyword fallback
		{"harden session permissions", "security"},
		{"security audit follow-up", "security"},
		{"implement caching layer", "feat"},
		{"add new dashboard widget", "feat"},
		{"build release artifacts", "feat"},
		{"repair broken import", "fix"},
		{"restore deleted config", "fix"},
		// no match
		{"tweak wording", "default"},
		{"", "default"},
	}
	for _, c := range cases {
		if got := ClassifyCommit(c.subject, cfg); got != c.want {
			t.Errorf("ClassifyCommit(%q) = %q, want %q", c.subject, got, c.want)
		}
	}
}

func TestCountLOCExclusions(t *testing.T) {
	cfg := testConfig()
	files := []FileStat{
		{Path: "main.go", Added: 100, Deleted: 20},               // counts: 120
		{Path: "go.sum", Added: 500, Deleted: 500},               // excluded
		{Path: "web/node_modules/x/index.js", Added: 999},        // excluded
		{Path: "vendor/lib/lib.go", Added: 50},                   // excluded
		{Path: "static/app.min.js", Added: 200},                  // excluded
		{Path: "static/app.js.map", Added: 200},                  // excluded
		{Path: "dist/bundle.js", Added: 300},                     // excluded
		{Path: "assets/logo.png", Added: 1, Deleted: 1},          // excluded
		{Path: "docs/spec.pdf", Added: 5},                        // excluded
		{Path: "data/dump.jsonl", Added: 1000},                   // excluded
		{Path: "package-lock.json", Added: 800},                  // excluded
		{Path: "internal/recap/recap.go", Added: 30, Deleted: 0}, // counts: 30
	}
	if got := countLOC(files, cfg); got != 150 {
		t.Errorf("countLOC = %d, want 150", got)
	}
}

func TestEstimateCommitLOCModifier(t *testing.T) {
	cfg := testConfig()
	// feat base = 3–8; 600 LOC → low += 600/200 = 3, high += 600/60 = 10
	c := RawCommit{
		Hash:    "abc123",
		Subject: "feat: big feature",
		Files:   []FileStat{{Path: "a.go", Added: 400, Deleted: 200}},
	}
	got := EstimateCommit(c, cfg)
	if got.Type != "feat" {
		t.Errorf("Type = %q, want feat", got.Type)
	}
	if got.LOC != 600 {
		t.Errorf("LOC = %d, want 600", got.LOC)
	}
	if got.HoursLow != 6 {
		t.Errorf("HoursLow = %v, want 6", got.HoursLow)
	}
	if got.HoursHigh != 18 {
		t.Errorf("HoursHigh = %v, want 18", got.HoursHigh)
	}
	if got.CostLow != 6*60 {
		t.Errorf("CostLow = %v, want %v", got.CostLow, 6*60.0)
	}
	if got.CostHigh != 18*175 {
		t.Errorf("CostHigh = %v, want %v", got.CostHigh, 18*175.0)
	}
}

func TestEstimateCommitCap(t *testing.T) {
	cfg := testConfig()
	// feat base high 8 + 3000/60 = 58 → capped at 24
	c := RawCommit{
		Hash:    "big",
		Subject: "feat: enormous change",
		Files:   []FileStat{{Path: "a.go", Added: 3000}},
	}
	got := EstimateCommit(c, cfg)
	if got.HoursHigh != cfg.MaxHoursPerCommit {
		t.Errorf("HoursHigh = %v, want cap %v", got.HoursHigh, cfg.MaxHoursPerCommit)
	}
	if got.CostHigh != cfg.MaxHoursPerCommit*cfg.RateHigh {
		t.Errorf("CostHigh = %v, want %v", got.CostHigh, cfg.MaxHoursPerCommit*cfg.RateHigh)
	}
}

func TestComputeEffortTotals(t *testing.T) {
	cfg := testConfig()
	commits := []RawCommit{
		// feat: 3–8, 0 LOC → 3–8h, $180–$1400
		{Hash: "1", Subject: "feat: one"},
		// fix: 1.5–5, 120 LOC → 1.5+0.6=2.1, 5+2=7 → $126–$1225
		{Hash: "2", Subject: "fix: two", Files: []FileStat{{Path: "b.go", Added: 100, Deleted: 20}}},
		// default: 1–4, excluded LOC only → 1–4h, $60–$700
		{Hash: "3", Subject: "wipe temp files", Files: []FileStat{{Path: "go.sum", Added: 999}}},
	}
	r := ComputeEffort(commits, cfg)
	if len(r.Commits) != 3 {
		t.Fatalf("len(Commits) = %d, want 3", len(r.Commits))
	}
	wantLow := 3.0 + 2.1 + 1.0
	wantHigh := 8.0 + 7.0 + 4.0
	if r.TotalHoursLow != wantLow {
		t.Errorf("TotalHoursLow = %v, want %v", r.TotalHoursLow, wantLow)
	}
	if r.TotalHoursHigh != wantHigh {
		t.Errorf("TotalHoursHigh = %v, want %v", r.TotalHoursHigh, wantHigh)
	}
	if r.TotalCostLow != wantLow*cfg.RateLow {
		t.Errorf("TotalCostLow = %v, want %v", r.TotalCostLow, wantLow*cfg.RateLow)
	}
	if r.TotalCostHigh != wantHigh*cfg.RateHigh {
		t.Errorf("TotalCostHigh = %v, want %v", r.TotalCostHigh, wantHigh*cfg.RateHigh)
	}
}

func TestParseGitLog(t *testing.T) {
	out := "@@@abc123\x00feat: add thing\n" +
		"10\t2\tmain.go\n" +
		"-\t-\tassets/logo.png\n" +
		"\n" +
		"@@@def456\x00fix: broken\n" +
		"5\t5\tlib/util.go\n"
	commits := parseGitLog(out)
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2", len(commits))
	}
	if commits[0].Hash != "abc123" || commits[0].Subject != "feat: add thing" {
		t.Errorf("commit[0] = %+v", commits[0])
	}
	if len(commits[0].Files) != 2 {
		t.Fatalf("commit[0] files = %d, want 2", len(commits[0].Files))
	}
	if commits[0].Files[0] != (FileStat{Path: "main.go", Added: 10, Deleted: 2}) {
		t.Errorf("file[0] = %+v", commits[0].Files[0])
	}
	// binary file ("-\t-") parses as zero counts
	if commits[0].Files[1] != (FileStat{Path: "assets/logo.png"}) {
		t.Errorf("file[1] = %+v", commits[0].Files[1])
	}
	if commits[1].Subject != "fix: broken" || len(commits[1].Files) != 1 {
		t.Errorf("commit[1] = %+v", commits[1])
	}
}
