package main

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/history"
)

func logCmdCmd() *cobra.Command {
	return &cobra.Command{
		Use: "log-cmd", Hidden: true, Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return err
			}
			return history.LogCommand(pid, args[1], args[2])
		},
	}
}
