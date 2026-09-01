package capture

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joecattt/thaw/internal/process"
	"github.com/joecattt/thaw/pkg/models"
)

// fakeDiscovery is a hand-rolled process.Discovery for tests — avoids touching
// the real OS process table so these tests are deterministic and fast.
type fakeDiscovery struct {
	shells   []process.ShellInfo
	cwd      map[int]string
	children map[int][]models.Process
	environ  map[int]map[string]string
	listErr  error
}

func (f *fakeDiscovery) ListShells() ([]process.ShellInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.shells, nil
}

func (f *fakeDiscovery) Children(pid int) ([]models.Process, error) {
	return f.children[pid], nil
}

func (f *fakeDiscovery) CWD(pid int) (string, error) {
	return f.cwd[pid], nil
}

func (f *fakeDiscovery) Environ(pid int) (map[string]string, error) {
	return f.environ[pid], nil
}

func TestFilterOutputLines_RedactsSecrets(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"password kv", "password=hunter2", "[redacted output line]"},
		{"token colon", "token: eyJabc123", "[redacted output line]"},
		{"api key", "API_KEY=sk-abc123def456", "[redacted output line]"},
		{"private key header", "-----BEGIN RSA PRIVATE KEY-----", "[redacted output line]"},
		{"jwt bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9", "[redacted output line]"},
		{"plain command", "$ ls -la /tmp", "$ ls -la /tmp"},
		{"plain output", "total 42 files copied successfully", "total 42 files copied successfully"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterOutputLines([]string{c.line})
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("filterOutputLines(%q) = %v, want [%q]", c.line, got, c.want)
			}
		})
	}
}

func TestOutputLineContainsSecret_LongBase64(t *testing.T) {
	// A long, high-entropy-looking token embedded in an otherwise plain line.
	line := "exported credential blob: QWxhZGRpbjpvcGVuIHNlc2FtZQpBbGFkZGluOm9wZW4gc2VzYW1lCg=="
	if !outputLineContainsSecret(line) {
		t.Error("expected long base64-like field to be flagged as a secret")
	}
}

func TestOutputLineContainsSecret_ShortStringsAreSafe(t *testing.T) {
	line := "npm install completed in 4.2s"
	if outputLineContainsSecret(line) {
		t.Error("did not expect a plain short line to be flagged")
	}
}

func TestLabeler_Match(t *testing.T) {
	l := NewLabeler(map[string]string{
		"npm run dev|npm start": "dev server",
		"psql":                  "database",
	})
	cases := []struct {
		command string
		want    string
	}{
		{"npm run dev", "dev server"},
		{"npm start", "dev server"},
		{"psql", "database"},
		{"totally unrelated", ""},
	}
	for _, c := range cases {
		if got := l.Match(c.command); got != c.want {
			t.Errorf("Match(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestSplitPipes(t *testing.T) {
	got := splitPipes("a|b|c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("splitPipes returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitPipes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// No pipe at all should return the whole string as a single-element slice.
	if got := splitPipes("solo"); len(got) != 1 || got[0] != "solo" {
		t.Errorf("splitPipes(solo) = %v, want [solo]", got)
	}
}

func TestLabelFromPath(t *testing.T) {
	if got := labelFromPath("/Users/alice/dev/thaw"); got != "thaw" {
		t.Errorf("labelFromPath = %q, want thaw", got)
	}
}

func TestEngine_IsExcludedPath(t *testing.T) {
	e := New(&fakeDiscovery{}, nil)
	e.SetExcludePaths([]string{"/private/", "/Users/alice/scratch"})

	cases := []struct {
		cwd  string
		want bool
	}{
		{"/private/tmp/xyz", true},
		{"/Users/alice/scratch/notes", true},
		{"/Users/alice/dev/thaw", false},
	}
	for _, c := range cases {
		if got := e.isExcludedPath(c.cwd); got != c.want {
			t.Errorf("isExcludedPath(%q) = %v, want %v", c.cwd, got, c.want)
		}
	}
}

func TestCaptureBaselineEnv_ParsesKeyValue(t *testing.T) {
	env := captureBaselineEnv()
	// PATH is present in essentially every process environment, including test runners.
	if _, ok := env["PATH"]; !ok {
		t.Error("expected PATH to be present in captured baseline env")
	}
}

func TestEngine_Capture_PropagatesDiscoveryError(t *testing.T) {
	e := New(&fakeDiscovery{listErr: errors.New("boom")}, nil)
	_, err := e.Capture("test")
	if err == nil {
		t.Fatal("expected Capture to return the discovery error, got nil")
	}
}

// TestCaptureSession_ScrubsCommandChildrenOutput covers the audit's proven
// bypasses: Command/Children were stored raw, and Output only passed the
// line-level heuristic — a bare PAT in scrollback reached disk unredacted.
func TestCaptureSession_ScrubsCommandChildrenOutput(t *testing.T) {
	disc := &fakeDiscovery{
		cwd: map[int]string{300: "/Users/alice/dev/thaw"},
		children: map[int][]models.Process{
			300: {{PID: 301, PPID: 300, Command: "mysql", Args: "mysql -u root -pSecret123"}},
		},
	}
	e := New(disc, nil)
	e.SetCaptureGit(false)
	e.SetCaptureEnv(false)

	// Short line: passes the length heuristic in outputLineContainsSecret,
	// so only scrub.Text stands between this token and disk.
	tmuxOut := map[string][]string{
		"/dev/ttys009": {"pat ghp_16C7e42F292c6912E7710c838347Ae178B4a ok"},
	}
	sess := e.captureSession(process.ShellInfo{PID: 300, TTY: "ttys009", Shell: "zsh"}, time.Now(), tmuxOut)

	if strings.Contains(sess.Children[0].Args, "Secret123") {
		t.Errorf("child args stored raw: %q", sess.Children[0].Args)
	}
	if strings.Contains(sess.Command, "Secret123") {
		t.Errorf("session command stored raw: %q", sess.Command)
	}
	joined := strings.Join(sess.Output, "\n")
	if strings.Contains(joined, "16C7e42F292c6912E7710c838347Ae178B4a") {
		t.Errorf("scrollback token stored raw: %q", joined)
	}
}

func TestEngine_Capture_SkipsInvalidAndExcludedSessions(t *testing.T) {
	disc := &fakeDiscovery{
		shells: []process.ShellInfo{
			{PID: 100, TTY: "ttys001", Shell: "zsh"},  // valid
			{PID: 0, TTY: "ttys002", Shell: "zsh"},    // invalid PID, should be dropped
			{PID: 200, TTY: "ttys003", Shell: "bash"}, // excluded by path
		},
		cwd: map[int]string{
			100: "/Users/alice/dev/thaw",
			200: "/private/tmp/excluded",
		},
	}
	e := New(disc, nil)
	e.SetExcludePaths([]string{"/private/"})
	e.SetCaptureGit(false)
	e.SetCaptureEnv(false)

	snap, err := e.Capture("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected exactly 1 valid session (PID 100), got %d: %+v", len(snap.Sessions), snap.Sessions)
	}
	if snap.Sessions[0].PID != 100 {
		t.Errorf("expected surviving session to be PID 100, got %d", snap.Sessions[0].PID)
	}
}
