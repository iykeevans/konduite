// Command agenthub is the local runtime connector.
package main

import (
	"fmt"
	"os"

	"github.com/iykeevans/konduite/runtime/internal/cli"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

func main() {
	cmd := cli.New(cli.Options{Version: version.Get(), Getenv: os.Getenv})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agenthub:", err)
		os.Exit(1)
	}
}
