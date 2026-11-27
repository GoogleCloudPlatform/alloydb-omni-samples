//go:build !windows

package connect

import (
	"os"
	"os/signal"
	"syscall"
)

// watch publishes a new size when the local terminal is resized.
// SIGWINCH does not exist on Windows, so that build has its own watch.
func (q *sizeQueue) watch() {
	defer close(q.ch)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)
	for {
		select {
		case <-q.done:
			return
		case <-sig:
			q.publish()
		}
	}
}
