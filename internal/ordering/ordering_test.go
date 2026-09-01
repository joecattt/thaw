package ordering

import (
	"testing"

	"github.com/joecattt/thaw/pkg/models"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		command string
		tier    int
	}{
		{"docker compose up", TierInfra},
		{"psql -U postgres mydb", TierInfra + 5},
		{"npm run dev", TierBackend},
		{"go run main.go", TierBackend},
		{"next dev", TierFrontend},
		{"npm test", TierTest},
		{"pytest", TierTest},
		{"tail -f /var/log/app.log", TierMonitor},
		{"vim main.go", TierEditor},
		{"ssh user@server.com", TierRemote},
	}

	for _, tt := range tests {
		s := models.Session{Command: tt.command, Shell: "zsh"}
		got := classify(s)
		if got != tt.tier {
			t.Errorf("classify(%q) = %d, want %d", tt.command, got, tt.tier)
		}
	}
}

func TestClassify_Idle(t *testing.T) {
	s := models.Session{Command: "zsh", Shell: "zsh"}
	got := classify(s)
	if got != TierIdle {
		t.Errorf("classify(idle) = %d, want %d", got, TierIdle)
	}
}

func TestSort_Order(t *testing.T) {
	sessions := []models.Session{
		{Command: "npm test", Shell: "zsh", RestoreOrder: TierTest},
		{Command: "docker compose up", Shell: "zsh", RestoreOrder: TierInfra},
		{Command: "npm run dev", Shell: "zsh", RestoreOrder: TierBackend},
	}

	sorted := Sort(sessions)

	if sorted[0].Command != "docker compose up" {
		t.Errorf("expected infra first, got %q", sorted[0].Command)
	}
	if sorted[1].Command != "npm run dev" {
		t.Errorf("expected backend second, got %q", sorted[1].Command)
	}
	if sorted[2].Command != "npm test" {
		t.Errorf("expected test third, got %q", sorted[2].Command)
	}
}
