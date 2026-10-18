package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cmd"
)

func main() {
	// First SIGINT/SIGTERM cancels ctx so commands can clean up.
	// stop() restores the default handler so a second Ctrl+C force-exits
	// if cleanup hangs (e.g. a stuck port-forward).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()

	err := cmd.NewRootCommand().ExecuteContext(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}
