package models

import "time"

// Process represents a single OS process.
type Process struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid"`
	Command string `json:"command"`
	Args    string `json:"args"`
	Status  string `json:"status"`
}

// GitState captures the repository state for a session's working directory.
type GitState struct {
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Dirty    bool   `json:"dirty"`
	Upstream string `json:"upstream,omitempty"`
	RepoRoot string `json:"repo_root"`
}

// EnvDelta holds environment variables that differ from the default inherited shell env.
type EnvDelta struct {
	Set   map[string]string `json:"set,omitempty"`
	Unset []string          `json:"unset,omitempty"`
}

func (e EnvDelta) IsEmpty() bool {
	return len(e.Set) == 0 && len(e.Unset) == 0
}

// StaleCheck records whether a session's context is still valid.
type StaleCheck struct {
	CWDExists      bool   `json:"cwd_exists"`
	BinaryExists   bool   `json:"binary_exists"`
	GitBranchMatch bool   `json:"git_branch_match"`
	Reachable      bool   `json:"reachable"`
	Reason         string `json:"reason,omitempty"`
}

func (sc StaleCheck) IsStale() bool {
	return !sc.CWDExists || !sc.BinaryExists
}

// Session represents a single terminal context at capture time.
type Session struct {
	PID        int       `json:"pid"`
	TTY        string    `json:"tty"`
	CWD        string    `json:"cwd"`
	Shell      string    `json:"shell"`
	Command    string    `json:"command"`
	Children   []Process `json:"children"`
	Label      string    `json:"label"`
	Status     string    `json:"status"`
	CapturedAt time.Time `json:"captured_at"`
	EnvDelta   EnvDelta  `json:"env_delta,omitempty"`
	Git        *GitState `json:"git,omitempty"`
	History    []string  `json:"history,omitempty"`
	GroupID    string    `json:"group_id,omitempty"`
	GroupName  string    `json:"group_name,omitempty"`
	Intent     string    `json:"intent,omitempty"`
	Output     []string  `json:"output,omitempty"`
	HasDirenv  bool      `json:"has_direnv,omitempty"`
	ProjectType string   `json:"project_type,omitempty"`
	RestoreOrder int     `json:"restore_order,omitempty"`

	// User-assigned tags for filtering (thaw tag api)
	Tags []string `json:"tags,omitempty"`

	// Whether this pane was focused at capture time
	Focused bool `json:"focused,omitempty"`
}

func (s Session) IsIdle() bool {
	return s.Command == "" || s.Command == s.Shell
}

func (s Session) HasTag(tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Snapshot is a point-in-time capture of all terminal sessions.
type Snapshot struct {
	ID        int       `json:"id"`
	Name      string    `json:"name,omitempty"`
	Sessions  []Session `json:"sessions"`
	CreatedAt time.Time `json:"created_at"`
	Source    string    `json:"source"`
	Hostname  string    `json:"hostname"`
	Intent    string    `json:"intent,omitempty"`

	// User annotations (thaw note "testing clock skew hypothesis")
	Notes []string `json:"notes,omitempty"`

	// Clipboard contents at capture time
	Clipboard string `json:"clipboard,omitempty"`

	// Browser tab URLs open at capture time
	BrowserTabs []string `json:"browser_tabs,omitempty"`

	// Hash chain for tamper detection (HMAC of prev hash + this snapshot data)
	PrevHash string `json:"prev_hash,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// DepRot describes dependency changes since a snapshot was taken.
type DepRot struct {
	File      string `json:"file"`
	Status    string `json:"status"` // modified | deleted | added
}

// ContextSwitch represents a project transition in the recap.
type ContextSwitch struct {
	Time     time.Time
	From     string
	To       string
	RampUpSec int // seconds until first meaningful command in new context
}

// Groups returns sessions organized by GroupID.
func (snap *Snapshot) Groups() map[string][]Session {
	groups := make(map[string][]Session)
	for _, s := range snap.Sessions {
		key := s.GroupID
		if key == "" {
			key = "_solo_" + s.TTY
		}
		groups[key] = append(groups[key], s)
	}
	return groups
}

// WorkstreamGroups returns only groups with 2+ sessions (real clusters).
// Solo sessions are collected under a "misc" key.
func (snap *Snapshot) WorkstreamGroups() map[string][]Session {
	raw := snap.Groups()
	result := make(map[string][]Session)
	var misc []Session

	for key, sessions := range raw {
		if len(sessions) >= 2 {
			// Use the group name as key for readability
			name := sessions[0].GroupName
			if name == "" {
				name = key
			}
			result[name] = sessions
		} else {
			misc = append(misc, sessions...)
		}
	}

	if len(misc) > 0 {
		result["misc"] = misc
	}
	return result
}

type RestoreMode int

const (
	SafeMode RestoreMode = iota
	RunMode
)

type RestoreOptions struct {
	Mode         RestoreMode
	SessionName  string
	Layout       string
	RestoreEnv   bool
	RestoreGit   bool
	ShowHistory  bool
	HistoryLines int
	SkipStale    bool
	MultiSession bool
	ShowOutput   bool
	ShowIntent   bool
	MaxPanes     int // max panes per tmux session before overflow to new window
	TierDelaySec int // seconds to sleep between dependency tiers in --run mode
}

func DefaultRestoreOptions() RestoreOptions {
	return RestoreOptions{
		Mode:         SafeMode,
		SessionName:  "thaw",
		Layout:       "tiled",
		RestoreEnv:   true,
		RestoreGit:   false,
		ShowHistory:  true,
		HistoryLines: 10,
		SkipStale:    false,
		MultiSession: true,
		ShowOutput:   true,
		ShowIntent:   true,
		MaxPanes:     8,
		TierDelaySec: 2,
	}
}
