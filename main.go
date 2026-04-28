package main

import (
	"fmt"
	"os"

	"gomall-cli/cmd"
	"gomall-cli/internal/clierr"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(clierr.ExitCode(err))
	}
}
