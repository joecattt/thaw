package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// FirebaseURL is the Firebase Realtime Database endpoint.
// Set this to your project URL before building, or leave empty to disable.
var FirebaseURL = ""

// Event types
const (
	EventInstall    = "install"
	EventCommand    = "command"
	EventRestore    = "restore"
	EventFreeze     = "freeze"
	EventError      = "error"
	EventFeature    = "feature"
	EventDailyPing  = "daily"
)

// Event is an anonymous telemetry data point.
type Event struct {
	ID        string    `json:"id"`         // anonymous device ID
	Event     string    `json:"event"`
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Shell     string    `json:"shell,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Data      EventData `json:"data,omitempty"`
}

// EventData holds optional structured fields — never PII.
type EventData struct {
	Command        string `json:"command,omitempty"`         // command name only (e.g. "freeze"), never args
	SessionCount   int    `json:"session_count,omitempty"`
	SnapshotCount  int    `json:"snapshot_count,omitempty"`
	ProjectCount   int    `json:"project_count,omitempty"`
	RestoreMode    string `json:"restore_mode,omitempty"`    // safe | run
	Success        bool   `json:"success,omitempty"`
	ErrorType      string `json:"error_type,omitempty"`      // category only, never message
	AIProvider     string `json:"ai_provider,omitempty"`     // claude | ollama | none
	BriefingTheme  string `json:"briefing_theme,omitempty"`
	FeaturesEnabled []string `json:"features_enabled,omitempty"` // list of enabled feature flags
	DurationMs     int64  `json:"duration_ms,omitempty"`
}

// IsEnabled returns true if telemetry is opt-in enabled.
func IsEnabled() bool {
	if FirebaseURL == "" {
		return false
	}
	optIn := filepath.Join(configDir(), "telemetry-optin")
	_, err := os.Stat(optIn)
	return err == nil
}

// OptIn enables telemetry and generates an anonymous ID.
func OptIn() error {
	dir := configDir()
	os.MkdirAll(dir, 0700)
	// Create opt-in marker
	if err := os.WriteFile(filepath.Join(dir, "telemetry-optin"), []byte("true\n"), 0644); err != nil {
		return err
	}
	// Generate anonymous device ID if not exists
	idFile := filepath.Join(dir, "telemetry-id")
	if _, err := os.Stat(idFile); os.IsNotExist(err) {
		b := make([]byte, 8)
		rand.Read(b)
		id := hex.EncodeToString(b)
		os.WriteFile(idFile, []byte(id), 0644)
	}
	return nil
}

// OptOut disables telemetry and removes the anonymous ID.
func OptOut() error {
	dir := configDir()
	os.Remove(filepath.Join(dir, "telemetry-optin"))
	os.Remove(filepath.Join(dir, "telemetry-id"))
	return nil
}

// Send submits a telemetry event asynchronously.
// Non-blocking — failures are silently dropped. Never affects CLI performance.
func Send(event string, version string, data EventData) {
	if !IsEnabled() {
		return
	}
	go func() {
		defer func() { recover() }() // never panic

		e := Event{
			ID:        getAnonymousID(),
			Event:     event,
			Version:   version,
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			Shell:     detectShell(),
			Timestamp: time.Now().UTC(),
			Data:      data,
		}

		body, err := json.Marshal(e)
		if err != nil {
			return
		}

		url := fmt.Sprintf("%s/events/%s/%s.json", FirebaseURL, e.ID, fmt.Sprintf("%d", time.Now().UnixMilli()))
		req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
}

// SendInstall records an install event with system info.
func SendInstall(version string) {
	Send(EventInstall, version, EventData{})
}

// SendCommand records which CLI command was used (name only, never args).
func SendCommand(version, cmdName string) {
	Send(EventCommand, version, EventData{Command: cmdName})
}

// SendError records an error category (never the full error message).
func SendError(version, errorType string) {
	Send(EventError, version, EventData{ErrorType: errorType})
}

// Status returns a human-readable telemetry status.
func Status() string {
	if !IsEnabled() {
		return "Telemetry: disabled (opt-in with: thaw config set telemetry true)"
	}
	return fmt.Sprintf("Telemetry: enabled (anonymous ID: %s...)", getAnonymousID()[:8])
}

func getAnonymousID() string {
	idFile := filepath.Join(configDir(), "telemetry-id")
	data, err := os.ReadFile(idFile)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "thaw")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "thaw")
}

func detectShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return "unknown"
	}
	return filepath.Base(shell)
}
