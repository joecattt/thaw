package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// Record is a flat row for CSV/JSON export.
type Record struct {
	SnapshotID int       `json:"snapshot_id"`
	Timestamp  time.Time `json:"timestamp"`
	Source     string    `json:"source"`
	SessionPID int       `json:"session_pid"`
	CWD        string    `json:"cwd"`
	Command    string    `json:"command"`
	Shell      string    `json:"shell"`
	GitBranch  string    `json:"git_branch,omitempty"`
	GitDirty   bool      `json:"git_dirty,omitempty"`
	GroupName  string    `json:"group_name,omitempty"`
	Intent     string    `json:"intent,omitempty"`
	Status     string    `json:"status"`
	History    string    `json:"history,omitempty"`
	Tags       string    `json:"tags,omitempty"`
}

// Flatten converts snapshots into flat export records.
func Flatten(snapshots []*models.Snapshot) []Record {
	var records []Record
	for _, snap := range snapshots {
		for _, s := range snap.Sessions {
			r := Record{
				SnapshotID: snap.ID,
				Timestamp:  snap.CreatedAt,
				Source:     snap.Source,
				SessionPID: s.PID,
				CWD:        s.CWD,
				Command:    s.Command,
				Shell:      s.Shell,
				GroupName:  s.GroupName,
				Intent:     s.Intent,
				Status:     s.Status,
			}
			if s.Git != nil {
				r.GitBranch = s.Git.Branch
				r.GitDirty = s.Git.Dirty
			}
			if len(s.History) > 0 {
				r.History = strings.Join(s.History, " ; ")
			}
			if len(s.Tags) > 0 {
				r.Tags = strings.Join(s.Tags, ",")
			}
			records = append(records, r)
		}
	}
	return records
}

// WriteCSV writes records as CSV to the given writer.
func WriteCSV(w io.Writer, records []Record) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header
	if err := cw.Write([]string{
		"snapshot_id", "timestamp", "source", "session_pid", "cwd",
		"command", "shell", "git_branch", "git_dirty", "group_name",
		"intent", "status", "history", "tags",
	}); err != nil {
		return err
	}

	for _, r := range records {
		dirty := ""
		if r.GitDirty {
			dirty = "true"
		}
		if err := cw.Write([]string{
			fmt.Sprintf("%d", r.SnapshotID),
			r.Timestamp.Format(time.RFC3339),
			r.Source,
			fmt.Sprintf("%d", r.SessionPID),
			r.CWD, r.Command, r.Shell, r.GitBranch, dirty,
			r.GroupName, r.Intent, r.Status, r.History, r.Tags,
		}); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON writes records as JSON to the given writer.
func WriteJSON(w io.Writer, records []Record) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(records)
}

// TimeAllocation computes hours per project directory from export records.
type ProjectTime struct {
	Dir     string        `json:"dir"`
	Name    string        `json:"name"`
	Branch  string        `json:"branch"`
	Count   int           `json:"session_count"`
	First   time.Time     `json:"first_seen"`
	Last    time.Time     `json:"last_seen"`
	EstHours float64      `json:"estimated_hours"`
}

// ComputeTimeAllocation groups records by CWD and estimates time spent.
func ComputeTimeAllocation(records []Record) []ProjectTime {
	byDir := make(map[string]*ProjectTime)

	for _, r := range records {
		pt, ok := byDir[r.CWD]
		if !ok {
			pt = &ProjectTime{
				Dir:   r.CWD,
				Name:  r.GroupName,
				First: r.Timestamp,
				Last:  r.Timestamp,
			}
			if pt.Name == "" {
				parts := strings.Split(r.CWD, "/")
				if len(parts) > 0 {
					pt.Name = parts[len(parts)-1]
				}
			}
			byDir[r.CWD] = pt
		}
		pt.Count++
		pt.Branch = r.GitBranch
		if r.Timestamp.Before(pt.First) {
			pt.First = r.Timestamp
		}
		if r.Timestamp.After(pt.Last) {
			pt.Last = r.Timestamp
		}
	}

	var result []ProjectTime
	for _, pt := range byDir {
		// Rough estimate: each snapshot interval represents ~15 min of active work
		pt.EstHours = float64(pt.Count) * 0.25
		result = append(result, *pt)
	}

	// Sort by count descending
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Count > result[i].Count {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}
