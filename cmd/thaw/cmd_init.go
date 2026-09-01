package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/project"
)

func initProjectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a .thaw.toml config file for this project",
		Long: `Detects the project type and creates a .thaw.toml with smart defaults.

  thaw init    creates .thaw.toml in the current directory`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := os.Getwd()

			// Check if .thaw.toml already exists
			if _, err := os.Stat(filepath.Join(dir, ".thaw.toml")); err == nil {
				fmt.Println(".thaw.toml already exists in this directory.")
				return nil
			}

			ptype := project.DetectProjectType(dir)
			name := filepath.Base(dir)

			var restoreCmds []string
			var testCmd string
			var envVars string
			var healthCheck string
			var buildCmd string

			switch ptype {
			case "node":
				restoreCmds = []string{"npm run dev"}
				testCmd = "npm test"
				envVars = "{ NODE_ENV = \"development\" }"
				healthCheck = "curl -sf http://localhost:3000/api/health"
				buildCmd = "npm run build"
			case "go":
				restoreCmds = []string{"go run ."}
				testCmd = "go test ./... -count=1"
				buildCmd = "go build ./..."
			case "python":
				restoreCmds = []string{"python manage.py runserver"}
				testCmd = "python -m pytest"
			case "rust":
				restoreCmds = []string{"cargo run"}
				testCmd = "cargo test"
				buildCmd = "cargo build"
			case "ruby":
				restoreCmds = []string{"bundle exec rails server"}
				testCmd = "bundle exec rspec"
			case "docker":
				restoreCmds = []string{"docker compose up -d"}
			default:
				restoreCmds = []string{"# add your dev server command"}
				testCmd = "# add your test command"
			}

			// Build TOML content
			var b strings.Builder
			fmt.Fprintf(&b, "# thaw project config — %s project\n", ptype)
			fmt.Fprintf(&b, "# Docs: https://github.com/joecattt/thaw#project-config\n\n")
			fmt.Fprintf(&b, "[project]\n")
			fmt.Fprintf(&b, "name = %q\n", name)

			fmt.Fprintf(&b, "restore_commands = [")
			for i, c := range restoreCmds {
				if i > 0 {
					fmt.Fprintf(&b, ", ")
				}
				fmt.Fprintf(&b, "%q", c)
			}
			fmt.Fprintf(&b, "]\n")

			if envVars != "" {
				fmt.Fprintf(&b, "env = %s\n", envVars)
			}
			if testCmd != "" {
				fmt.Fprintf(&b, "test_command = %q\n", testCmd)
			}
			if healthCheck != "" {
				fmt.Fprintf(&b, "health_check = %q\n", healthCheck)
			}
			if buildCmd != "" {
				fmt.Fprintf(&b, "build_command = %q\n", buildCmd)
			}
			fmt.Fprintf(&b, "todo_pattern = \"TODO|FIXME|HACK|XXX\"\n")

			path := filepath.Join(dir, ".thaw.toml")
			if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
				return fmt.Errorf("writing .thaw.toml: %w", err)
			}

			fmt.Printf("Created .thaw.toml (%s project)\n", ptype)
			fmt.Printf("  name: %s\n", name)
			fmt.Printf("  restore: %v\n", restoreCmds)
			if testCmd != "" {
				fmt.Printf("  test: %s\n", testCmd)
			}
			fmt.Printf("\nEdit %s to customize.\n", path)
			return nil
		},
	}
}
