package connect

import (
	"io"
	"os"
	"sync"

	"golang.org/x/term"
	"k8s.io/client-go/tools/remotecommand"
)

// withTerminal puts a local terminal into raw mode for the duration of fn and
// reports its size to the remote TTY. Non-terminals call fn unchanged.
func withTerminal(stdin io.Reader, tty bool, fn func(remotecommand.TerminalSizeQueue) error) error {
	if !tty {
		return fn(nil)
	}
	f, ok := stdin.(*os.File)
	if !ok || f == nil || !term.IsTerminal(int(f.Fd())) {
		return fn(nil)
	}
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, state)

	sizes := newSizeQueue(fd)
	defer sizes.stop()
	return fn(sizes)
}

// sizeQueue feeds the remote TTY an initial size and later SIGWINCH updates.
type sizeQueue struct {
	fd   int
	ch   chan remotecommand.TerminalSize
	done chan struct{}
	once sync.Once
}

func newSizeQueue(fd int) *sizeQueue {
	q := &sizeQueue{
		fd:   fd,
		ch:   make(chan remotecommand.TerminalSize, 1),
		done: make(chan struct{}),
	}
	q.publish()
	go q.watch()
	return q
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

func (q *sizeQueue) stop() {
	q.once.Do(func() { close(q.done) })
}

func (q *sizeQueue) publish() {
	w, h, err := term.GetSize(q.fd)
	if err != nil || w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	size := remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}
	select {
	case q.ch <- size:
		return
	default:
	}
	select {
	case <-q.ch:
	default:
	}
	select {
	case q.ch <- size:
	default:
	}
}
