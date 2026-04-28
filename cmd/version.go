package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build metadata",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("version=%s commit=%s date=%s\n", version, commit, date)
		},
	}
}
