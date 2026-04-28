package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newHelloCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hello [name]",
		Short: "Print a hello message",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := "world"
			if len(args) == 1 {
				name = args[0]
			}
			fmt.Printf("hello, %s!\n", name)
		},
	}
}
