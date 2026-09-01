package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/telemetry"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or change thaw settings",
	}
	cmd.AddCommand(&cobra.Command{
		Use: "show", Short: "Print current config (TOML — same format as the config file)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// TOML, not JSON: matches the on-disk config file and lets shell hooks
			// grep for `key = true` (the morning-briefing/autostash gates rely on this).
			return toml.NewEncoder(os.Stdout).Encode(cfg)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "get <key>", Short: "Print a single config value (for scripts/hooks)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			v, err := config.GetField(cfg, args[0])
			if err != nil {
				return err
			}
			fmt.Println(v)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "check", Short: "Report unknown keys and invalid values in the config",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, warnings, err := config.LoadWithWarnings()
			if err != nil {
				return err
			}
			if len(warnings) == 0 {
				fmt.Println("thaw: config OK")
				return nil
			}
			for _, w := range warnings {
				fmt.Fprintf(os.Stderr, "thaw config warning: %s\n", w)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print config file path",
		Run: func(cmd *cobra.Command, args []string) {
			p, _ := config.ConfigPath()
			fmt.Println(p)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long: `Set a configuration value. Keys use dot notation.

  thaw config set ai.provider claude
  thaw config set ai.model claude-sonnet-4-20250514
  thaw config set briefing.theme frost
  thaw config set briefing.priority_order blocked
  thaw config set capture.idle_threshold_min 30
  thaw config set daemon.interval_min 10
  thaw config set telemetry.enabled true
  thaw config set voice.tts_backend say
  thaw config set news.sources hn,bbc,gizmodo,techcrunch`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, val := args[0], args[1]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := config.SetField(&cfg, key, val); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			// Handle telemetry opt-in/out
			if key == "telemetry.enabled" {
				if val == "true" {
					telemetry.OptIn()
					if cfg.Telemetry.FirebaseURL != "" {
						telemetry.FirebaseURL = cfg.Telemetry.FirebaseURL
					}
					fmt.Println("Telemetry enabled (anonymous, opt-in)")
				} else {
					telemetry.OptOut()
					fmt.Println("Telemetry disabled")
				}
				return nil
			}
			fmt.Printf("Set %s = %s\n", key, val)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "reset", Short: "Reset config to defaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.DefaultConfig()
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println("Config reset to defaults.")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "validate", Short: "Check config for errors",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				fmt.Printf("Config error: %s\n", err)
				return nil
			}
			warnings := config.Validate(cfg)
			if len(warnings) == 0 {
				fmt.Println("Config is valid.")
			} else {
				for _, w := range warnings {
					fmt.Printf("  ⚠ %s\n", w)
				}
			}
			return nil
		},
	})
	return cmd
}
