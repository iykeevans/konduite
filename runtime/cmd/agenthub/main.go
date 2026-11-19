// Command agenthub is the local runtime connector.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/iykeevans/konduite/runtime/internal/cli"
	"github.com/iykeevans/konduite/runtime/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	cmd := cli.New(cli.Options{Version: version.Get(), Getenv: os.Getenv})
	err := cmd.ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agenthub:", err)
		os.Exit(1)
	}
}
