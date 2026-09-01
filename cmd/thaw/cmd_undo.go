package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/restore"
)

func undoCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Close the tmux sessions opened by the last restore",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Println("This will kill all tmux sessions from the last restore.")
				fmt.Print("Continue? (use --yes to skip this prompt) [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" && answer != "yes" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			killed, err := restore.Undo()
			if err != nil {
				return err
			}
			if killed == 0 {
				fmt.Println("No sessions to undo — the last restore didn't leave any tmux sessions open.")
			} else {
				fmt.Printf("Killed %d tmux session(s)\n", killed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}
