package briefing

import "time"

// ConvosForProject is the exported entry point dashboard uses to pull real,
// token-priced AI-spend data for one project directory — same parsing/pricing
// engine loadClaudeConvos already uses for the resume-screen briefing card,
// just with a caller-supplied lookback instead of the briefing's fixed window.
func ConvosForProject(cwd string, since time.Time) []ClaudeConvo {
	window := time.Since(since)
	if window <= 0 {
		return nil
	}
	return loadClaudeConvos(cwd, window, 100000) // no practical convo-count cap
}
