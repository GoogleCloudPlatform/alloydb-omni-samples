package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/adbo/internal/cmd"
)


func main() {

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := cmd.NewRootCommand().ExecuteContext(ctx)
	stop()

	if err != nil {
		os.Exit(1)
	}
}