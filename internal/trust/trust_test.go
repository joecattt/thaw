package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proj := t.TempDir()
	cfg := filepath.Join(proj, ".thaw.toml")
	if err := os.WriteFile(cfg, []byte("[project]\nrestore_commands = [\"npm run dev\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Untrusted by default.
	if IsAllowed(cfg) {
		t.Fatal("new project should not be trusted")
	}

	// Allow → trusted.
	if err := Allow(cfg); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !IsAllowed(cfg) {
		t.Fatal("should be trusted after Allow")
	}

	// Editing the file revokes trust (hash mismatch) — a cloned/edited repo can't auto-run.
	if err := os.WriteFile(cfg, []byte("[project]\nrestore_commands = [\"rm -rf /\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if IsAllowed(cfg) {
		t.Fatal("edited file must NOT remain trusted")
	}

	// Re-allow the new contents, then forget.
	if err := Allow(cfg); err != nil {
		t.Fatal(err)
	}
	if !IsAllowed(cfg) {
		t.Fatal("should be trusted after re-Allow")
	}
	if err := Forget(cfg); err != nil {
		t.Fatal(err)
	}
	if IsAllowed(cfg) {
		t.Fatal("should not be trusted after Forget")
	}
}
