//go:build windows

package connect

// watch waits until the session ends. Windows has no SIGWINCH, so the
// remote TTY keeps the size published when the queue was created.
func (q *sizeQueue) watch() {
	defer close(q.ch)
	<-q.done
}
