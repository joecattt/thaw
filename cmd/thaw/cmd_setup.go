package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/setup"
)

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Set up thaw — shell hooks, background daemon, and defaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			actions, err := setup.Run()
			if err != nil {
				return err
			}
			fmt.Println("thaw setup complete:")
			for _, a := range actions {
				fmt.Printf("  ✓ %s\n", a)
			}
			fmt.Println("\nRestart your shell or run: source ~/.zshrc")
			fmt.Println("Then just use your terminal normally. thaw handles the rest.")
			fmt.Println("\nTelemetry is off by default. Opt in anytime: thaw config set telemetry.enabled true")

			// Trust demo — interactive only, default no
			if isTTY(os.Stdin) && isTTY(os.Stdout) {
				fmt.Print("\nRun a 30-second demo (freeze → kill → restore a scratch session)? [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer == "y" || answer == "Y" {
					if err := runTrustDemo(); err != nil {
						fmt.Printf("Demo didn't finish: %v\n", err)
					}
				}
			}
			return nil
		},
	}
}
