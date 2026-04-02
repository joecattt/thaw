package history

import (
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
