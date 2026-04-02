package atuin

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// Available returns true if atuin's history database exists.
func Available() bool {
	_, err := os.Stat(dbPath())
	return err == nil
}

// HistoryForSession returns the most recent commands for a shell PID from atuin's database.
// Atuin stores session_id which correlates to a shell session.
// We match by CWD + recent timestamps as a heuristic when PID isn't directly available.
func HistoryForSession(cwd string, limit int) []string {
	db, err := sql.Open("sqlite3", dbPath())
	if err != nil {
		return nil
	}
	defer db.Close()

	// Atuin schema: history table with columns: id, timestamp, duration, exit, command, cwd, session, hostname
	// Query recent commands matching this CWD
	rows, err := db.Query(`
		SELECT command FROM history
		WHERE cwd = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, cwd, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var cmds []string
	for rows.Next() {
		var cmd string
		if rows.Scan(&cmd) == nil && cmd != "" {
			cmds = append(cmds, cmd)
		}
	}

	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(cmds)-1; i < j; i, j = i+1, j-1 {
		cmds[i], cmds[j] = cmds[j], cmds[i]
	}

	return cmds
}

// RecentGlobal returns the N most recent commands across all sessions.
func RecentGlobal(limit int) []string {
	db, err := sql.Open("sqlite3", dbPath())
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT command FROM history
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var cmds []string
	for rows.Next() {
		var cmd string
		if rows.Scan(&cmd) == nil && cmd != "" {
			cmds = append(cmds, cmd)
		}
	}

	for i, j := 0, len(cmds)-1; i < j; i, j = i+1, j-1 {
		cmds[i], cmds[j] = cmds[j], cmds[i]
	}
	return cmds
}

// dbPath returns the atuin database location.
func dbPath() string {
	// Check ATUIN_DB_PATH first
	if p := os.Getenv("ATUIN_DB_PATH"); p != "" {
		return p
	}

	// Default XDG location
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "atuin", "history.db")
}
