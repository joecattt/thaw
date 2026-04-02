package context

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// Metrics summarizes context-switching behavior over a time period.
type Metrics struct {
	Period       string
	Switches     int
	AvgRampUp    time.Duration
	TotalOverhead time.Duration
	TopProjects  []ProjectTime
	SwitchLog    []Switch
}

type ProjectTime struct {
	Name     string
	Duration time.Duration
}

type Switch struct {
	Time   time.Time
	From   string
	To     string
	RampUp time.Duration
}

// Compute analyzes snapshots for context-switching patterns.
func Compute(store *snapshot.Store, from, to time.Time) (*Metrics, error) {
	summaries, err := store.List(1000)
	if err != nil {
		return nil, err
	}

	// Filter to date range — only load what we need
	var snaps []*models.Snapshot
	loaded := 0
	for _, s := range summaries {
		if s.CreatedAt.Before(from) || s.CreatedAt.After(to) {
			continue
		}
		if loaded >= 500 { // cap to prevent memory issues
			break
		}
		snap, err := store.Get(s.ID)
		if err != nil || snap == nil {
			continue
		}
		snaps = append(snaps, snap)
		loaded++
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})

	if len(snaps) < 2 {
		return &Metrics{Period: formatPeriod(from, to)}, nil
	}

	m := &Metrics{Period: formatPeriod(from, to)}
	projectTime := make(map[string]time.Duration)

	var lastProject string
	var lastSwitchTime time.Time

	for i, snap := range snaps {
		project := primaryProject(snap)

		// Track project time
		if i > 0 {
			dt := snap.CreatedAt.Sub(snaps[i-1].CreatedAt)
			if dt < 30*time.Minute { // skip gaps > 30 min (lunch, meetings)
				projectTime[lastProject] += dt
			}
		}

		if project != lastProject && lastProject != "" {
			// Estimate ramp-up: time between switch and first "meaningful" snapshot
			// (one where sessions > 1 or command isn't idle)
			rampUp := estimateRampUp(snaps, i)

			m.Switches++
			m.SwitchLog = append(m.SwitchLog, Switch{
				Time:   snap.CreatedAt,
				From:   lastProject,
				To:     project,
				RampUp: rampUp,
			})
			m.TotalOverhead += rampUp
			lastSwitchTime = snap.CreatedAt
		}
		_ = lastSwitchTime

		lastProject = project
	}

	// Average ramp-up
	if m.Switches > 0 {
		m.AvgRampUp = m.TotalOverhead / time.Duration(m.Switches)
	}

	// Top projects by time
	for name, dur := range projectTime {
		m.TopProjects = append(m.TopProjects, ProjectTime{Name: name, Duration: dur})
	}
	sort.Slice(m.TopProjects, func(i, j int) bool {
		return m.TopProjects[i].Duration > m.TopProjects[j].Duration
	})

	return m, nil
}

// FormatMetrics returns a human-readable metrics report.
func FormatMetrics(m *Metrics) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Context switching — %s\n\n", m.Period))

	if m.Switches == 0 {
		b.WriteString("No context switches detected. Deep work session.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("  Switches:       %d\n", m.Switches))
	b.WriteString(fmt.Sprintf("  Avg ramp-up:    %s\n", formatDur(m.AvgRampUp)))
	b.WriteString(fmt.Sprintf("  Total overhead: %s\n", formatDur(m.TotalOverhead)))
	b.WriteString("\n")

	if len(m.TopProjects) > 0 {
		b.WriteString("  Time by project:\n")
		for _, p := range m.TopProjects {
			b.WriteString(fmt.Sprintf("    %s — %s\n", p.Name, formatDur(p.Duration)))
		}
		b.WriteString("\n")
	}

	if len(m.SwitchLog) > 0 {
		b.WriteString("  Switch log:\n")
		for _, s := range m.SwitchLog {
			b.WriteString(fmt.Sprintf("    %s  %s → %s (%s ramp-up)\n",
				s.Time.Format("3:04 PM"), s.From, s.To, formatDur(s.RampUp)))
		}
	}

	return b.String()
}

func estimateRampUp(snaps []*models.Snapshot, switchIdx int) time.Duration {
	if switchIdx >= len(snaps)-1 {
		return 0
	}
	// Ramp-up = time to next snapshot that has a non-idle session
	switchTime := snaps[switchIdx].CreatedAt
	for i := switchIdx + 1; i < len(snaps) && i < switchIdx+4; i++ {
		for _, s := range snaps[i].Sessions {
			if !s.IsIdle() {
				return snaps[i].CreatedAt.Sub(switchTime)
			}
		}
	}
	// Default: time to next snapshot
	return snaps[switchIdx+1].CreatedAt.Sub(switchTime)
}

func primaryProject(snap *models.Snapshot) string {
	for _, s := range snap.Sessions {
		if s.Git != nil && s.Git.RepoRoot != "" {
			parts := strings.Split(s.Git.RepoRoot, "/")
			return parts[len(parts)-1]
		}
	}
	if len(snap.Sessions) > 0 {
		parts := strings.Split(snap.Sessions[0].CWD, "/")
		return parts[len(parts)-1]
	}
	return "unknown"
}

func formatPeriod(from, to time.Time) string {
	if from.Format("2006-01-02") == to.Format("2006-01-02") {
		return from.Format("2006-01-02")
	}
	return from.Format("Jan 2") + " – " + to.Format("Jan 2")
}

func formatDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}
