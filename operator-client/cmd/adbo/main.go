package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cmd"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

func main() {
	// client-go's reflector logs through klog, and warns on stderr whenever a
	// watch ends early -- including the cancellations adbo performs on purpose
	// when a wait finishes or fails fast. Send klog nowhere so command output
	// stays clean; real errors reach the user through the returned error.
	klog.LogToStderr(false)
	klog.SetOutput(io.Discard)

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
		fmt.Fprintln(os.Stderr, "Error:", k8s.Format(err, "", ""))
		os.Exit(1)
	}
}
