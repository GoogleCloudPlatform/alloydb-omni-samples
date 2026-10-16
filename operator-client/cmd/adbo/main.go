package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/adbo/internal/cmd"
)


func main() {

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)

	err := cmd.NewRootCommand().ExecuteContext(ctx)
	stop()

	if err != nil {
		os.Exit(1)
	}
}