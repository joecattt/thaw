package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/trust"
)

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "run",
		Short:   "Run this project's .thaw.toml restore_commands (dev server, etc.) — trust-gated",
		GroupID: "everyday",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfgPath := project.Find(cwd)
			if cfgPath == "" {
				return fmt.Errorf("no .thaw.toml found here — run `thaw init` to create one")
			}
			pc, err := project.Load(cwd)
			if err != nil {
				return err
			}
			cmds := pc.Project.RestoreCommands
			if len(cmds) == 0 {
				fmt.Println("thaw: no restore_commands in .thaw.toml")
				return nil
			}
			// Trust gate: never auto-run commands from an untrusted or edited project file.
			if !trust.IsAllowed(cfgPath) {
				fmt.Printf("thaw: %s is not trusted. Its restore_commands are:\n\n", cfgPath)
				for _, c := range cmds {
					fmt.Printf("    %s\n", c)
				}
				fmt.Printf("\nReview them, then run `thaw allow` here to permit auto-run.\n")
				return fmt.Errorf("untrusted project config")
			}
			for k, v := range pc.Project.Env {
				os.Setenv(k, v)
			}
			for _, c := range cmds {
				fmt.Printf("thaw: $ %s\n", c)
				ec := exec.Command("sh", "-c", c)
				ec.Dir = cwd
				ec.Stdout, ec.Stderr, ec.Stdin = os.Stdout, os.Stderr, os.Stdin
				if err := ec.Run(); err != nil {
					return fmt.Errorf("command failed (%q): %w", c, err)
				}
			}
			return nil
		},
	}
}
