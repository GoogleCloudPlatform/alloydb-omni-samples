package connect

import (
	"strings"
	"testing"

	"k8s.io/client-go/tools/remotecommand"
)

func TestWithTerminalSkipsNonTerminal(t *testing.T) {
	called := false
	err := withTerminal(strings.NewReader("x"), true, func(q remotecommand.TerminalSizeQueue) error {
		called = true
		if q != nil {
			t.Fatal("non-terminal stdin should not publish a size")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("callback was not called")
	}
}

func TestSizeQueuePublishesAndStops(t *testing.T) {
	q := newSizeQueue(-1)
	got := q.Next()
	if got == nil || got.Width != 80 || got.Height != 24 {
		t.Fatalf("size %#v", got)
	}
	q.stop()
	if next := q.Next(); next != nil {
		t.Fatalf("after stop %#v", next)
	}
}
