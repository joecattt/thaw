package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubCommand(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"git status", "git status"},
		{"mysql -u root -p secretpass", "mysql -u root -p [redacted]"},
		{"psql --password=hunter2 mydb", "psql --password=[redacted] mydb"},
		{"npm run dev", "npm run dev"},
		{"ssh -i ~/.ssh/key user@host", "ssh -i ~/.ssh/key user@host"},
		{"docker login --token=abc123", "docker login --token=[redacted]"},
		// Enhanced: Bearer tokens now redacted
		{"curl -H Authorization: Bearer eyJhbGciOiJ http://api.com", "curl -H Authorization: Bearer [redacted] http://api.com"},
		// Enhanced: export with secret-looking key names now redacted
		{"export API_KEY=sk-1234", "export API_KEY=[redacted]"},
		{"export NODE_ENV=production", "export NODE_ENV=production"}, // non-secret key passes through
	}

	for _, tt := range tests {
		got := scrubCommand(tt.input)
		if got != tt.expect {
			t.Errorf("scrubCommand(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestIsTrivialCommand(t *testing.T) {
	trivial := []string{"ls", "pwd", "clear", "whoami", "date", "history", ""}
	for _, cmd := range trivial {
		if !isTrivialCommand(cmd) {
			t.Errorf("expected trivial: %q", cmd)
		}
	}

	meaningful := []string{
		"git status", "npm run dev", "cd /project", "vim main.go",
		"docker compose up", "go test ./...",
	}
	for _, cmd := range meaningful {
		if isTrivialCommand(cmd) {
			t.Errorf("expected meaningful: %q", cmd)
		}
	}
}

// TestLogCommandScrubsToDisk verifies the full hook path: LogCommand runs BOTH scrubbers
// (flag-aware + regex) before writing, so secrets the flag scrubber alone misses (JWTs, AWS
// keys, PEM blocks) never reach disk — and the file is 0600.
func TestLogCommandScrubsToDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	logPath := filepath.Join(stateDir(), "commands.log")

	// A raw JWT with no secret flag — only the regex layer (scrub.Text) catches this.
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abcDEF123456"
	if err := LogCommand(4242, "/tmp/proj", "curl -H 'Authorization: Bearer "+jwt+"' https://api.example.com"); err != nil {
		t.Fatalf("LogCommand: %v", err)
	}
	if err := LogCommand(4242, "/tmp/proj", "aws configure set key AKIAIOSFODNN7EXAMPLE"); err != nil {
		t.Fatalf("LogCommand: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(data)
	if strings.Contains(got, jwt) {
		t.Errorf("JWT leaked to disk:\n%s", got)
	}
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key leaked to disk:\n%s", got)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("commands.log perms = %o, want 600", perm)
	}
}

// TestPurgeSecrets verifies retroactive re-scrubbing of a log written raw by an older build.
func TestPurgeSecrets(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "commands.log")
	raw := "1700000000|99|/p|export API_KEY=sk-supersecretvalue1234567890abcd\n" +
		"1700000001|99|/p|git status\n"
	if err := os.WriteFile(logPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := PurgeSecrets()
	if err != nil {
		t.Fatalf("PurgeSecrets: %v", err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "sk-supersecretvalue1234567890abcd") {
		t.Errorf("secret survived purge:\n%s", data)
	}
	if !strings.Contains(string(data), "git status") {
		t.Errorf("non-secret line was mangled:\n%s", data)
	}
}
