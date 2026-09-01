package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/briefing"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/context"
	"github.com/joecattt/thaw/internal/recap"
	"github.com/joecattt/thaw/internal/snapshot"
)

func recapCmd() *cobra.Command {
	var (
		voice    bool
		visual   bool
		brief    bool
		frost    bool
		full     bool
		metrics  bool
		noEffort bool
	)

	cmd := &cobra.Command{
		Use:   "recap [today|yesterday|week]",
		Short: "Summarize your recent work — text, voice, or visual timeline",
		Long: `Generate a recap of your work activity from snapshot history.

  thaw recap              today's work summary
  thaw recap yesterday    yesterday's recap
  thaw recap week         weekly rollup
  thaw recap --voice      spoken summary (macOS say / Linux espeak)
  thaw recap --visual     open HTML timeline in browser
  thaw recap --brief      15-second flash briefing`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load()
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			// Determine date range
			now := time.Now()
			var from, to time.Time
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}

			switch period {
			case "today", "":
				from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
				to = now
			case "yesterday":
				y := now.AddDate(0, 0, -1)
				from = time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, y.Location())
				to = time.Date(y.Year(), y.Month(), y.Day(), 23, 59, 59, 0, y.Location())
			case "week":
				weekday := int(now.Weekday())
				if weekday == 0 {
					weekday = 7
				}
				monday := now.AddDate(0, 0, -(weekday - 1))
				from = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, monday.Location())
				to = now
			default:
				// Try parsing as date
				t, err := time.Parse("2006-01-02", period)
				if err != nil {
					return fmt.Errorf("unknown period: %s (use today, yesterday, week, or YYYY-MM-DD)", period)
				}
				from = t
				to = t.Add(24*time.Hour - time.Second)
			}

			r, err := recap.Generate(store, from, to)
			if err != nil {
				fmt.Println("No work activity found for that period.")
				return nil
			}
			if noEffort {
				r.Effort = nil
			}

			// Frost briefing — full cinematic dashboard
			if frost {
				snap, err := store.Latest()
				if err != nil || snap == nil {
					return fmt.Errorf("no snapshot available for briefing")
				}
				path, err := briefing.Generate(snap, cfg)
				if err != nil {
					return err
				}
				fmt.Printf("Briefing: %s\n", path)
				return briefing.Open(path)
			}

			// Visual mode — open in browser
			if visual {
				path, err := recap.GenerateHTML(r)
				if err != nil {
					return err
				}
				fmt.Printf("Timeline saved to %s\n", path)
				return recap.OpenInBrowser("file://" + path)
			}

			// Voice mode
			if voice {
				var text string
				if brief {
					text = recap.FormatVoiceBrief(r)
				} else if full {
					text = recap.FormatVoiceFull(r)
				} else {
					// Default: brief first, then ask
					text = recap.FormatVoiceBrief(r)
					fmt.Println(recap.FormatText(r))
					fmt.Println("\nSpeaking brief summary...")
				}
				return recap.SpeakWithConfig(text, recap.VoiceConfig{
					Backend: cfg.Voice.Backend,
				})
			}

			// Text mode (default)
			fmt.Print(recap.FormatText(r))

			// If brief requested, also print the voice version
			if brief {
				fmt.Println("\n" + recap.FormatVoiceBrief(r))
			}

			// Context switching metrics
			if metrics {
				m, err := context.Compute(store, from, to)
				if err == nil {
					fmt.Println()
					fmt.Print(context.FormatMetrics(m))
				}
			}

			// AI gap analysis — "what should I do next"
			if cfg.AI.GapAnalysis && cfg.AI.Provider != "none" {
				lc := newLLMClient(cfg)
				if lc.Available() {
					ctx := recap.FormatText(r)
					suggestion, err := lc.GapAnalysis(ctx)
					if err == nil && suggestion != "" {
						fmt.Println("\n  Next actions:")
						for _, line := range strings.Split(strings.TrimSpace(suggestion), "\n") {
							if line != "" {
								fmt.Printf("    → %s\n", strings.TrimSpace(line))
							}
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&voice, "voice", false, "Speak the recap via TTS")
	cmd.Flags().BoolVar(&visual, "visual", false, "Open HTML timeline in browser")
	cmd.Flags().BoolVar(&frost, "briefing", false, "Open frost briefing dashboard")
	cmd.Flags().BoolVar(&brief, "brief", false, "15-second flash briefing")
	cmd.Flags().BoolVar(&full, "full", false, "Full detailed recap (with --voice)")
	cmd.Flags().BoolVar(&metrics, "metrics", false, "Show context-switching metrics")
	cmd.Flags().BoolVar(&noEffort, "no-effort", false, "Hide labor-cost effort estimate")
	return cmd
}
