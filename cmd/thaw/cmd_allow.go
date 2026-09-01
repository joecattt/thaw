package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/trust"
)

func allowCmd() *cobra.Command {
	var forget bool
	c := &cobra.Command{
		Use:     "allow",
		Short:   "Trust this project's .thaw.toml so `thaw run` can execute its commands",
		GroupID: "setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfgPath := project.Find(cwd)
			if cfgPath == "" {
				return fmt.Errorf("no .thaw.toml found here")
			}
			if forget {
				if err := trust.Forget(cfgPath); err != nil {
					return err
				}
				fmt.Printf("thaw: revoked trust for %s\n", cfgPath)
				return nil
			}
			if err := trust.Allow(cfgPath); err != nil {
				return err
			}
			fmt.Printf("thaw: trusted %s — `thaw run` will now execute its restore_commands\n", cfgPath)
			return nil
		},
	}
	c.Flags().BoolVar(&forget, "forget", false, "revoke trust instead of granting it")
	return c
}
