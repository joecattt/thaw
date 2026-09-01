// Package trust gates execution of project-defined commands (.thaw.toml restore_commands)
// behind an explicit, direnv-style allowlist. A project's commands only run after the user
// has run `thaw allow` in it, and only while the file's contents are unchanged — editing
// .thaw.toml revokes trust until re-allowed. This prevents a cloned/untrusted repo from
// running arbitrary commands the moment you cd into it or restore a session.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "thaw")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/var/tmp", fmt.Sprintf("thaw-%d", os.Getuid()))
	}
	return filepath.Join(home, ".local", "state", "thaw")
}

func allowPath() string { return filepath.Join(stateDir(), "allowed.json") }

// Hash returns the SHA-256 of the file's contents, hex-encoded.
func Hash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func load() map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(allowPath())
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	return m
}

func save(m map[string]string) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(allowPath(), data, 0600)
}

// IsAllowed reports whether path is trusted AND unchanged since it was allowed.
func IsAllowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	want, ok := load()[abs]
	if !ok {
		return false
	}
	got, err := Hash(abs)
	if err != nil {
		return false
	}
	return got == want
}

// Allow records the current contents of path as trusted.
func Allow(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	h, err := Hash(abs)
	if err != nil {
		return err
	}
	m := load()
	m[abs] = h
	return save(m)
}

// Forget revokes trust for path.
func Forget(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	m := load()
	delete(m, abs)
	return save(m)
}
