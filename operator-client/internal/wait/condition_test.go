package wait

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// pushLW Lists obj, then hands out a watch the test can send events on.
type pushLW struct {
	staticLW
	w *watch.FakeWatcher
}

func (p *pushLW) Watch(metav1.ListOptions) (watch.Interface, error) { return p.w, nil }

func phaseIs(want string) Condition {
	return func(obj *unstructured.Unstructured) (bool, error) { return Phase(obj) == want, nil }
}

func TestUntilConditionAlreadyTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := untilCondition(ctx, &staticLW{obj: backup("b1", "Done", "")}, "b1", phaseIs("Done"))
	if err != nil || Phase(got) != "Done" {
		t.Fatalf("got %v, %v", Phase(got), err)
	}
}

func TestUntilConditionWatchEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lw := &pushLW{staticLW: staticLW{obj: backup("b1", "Working", "")}, w: watch.NewFake()}
	go func() {
		obj := backup("b1", "Done", "")
		obj.SetResourceVersion("2")
		lw.w.Modify(obj)
	}()
	got, err := untilCondition(ctx, lw, "b1", phaseIs("Done"))
	if err != nil || Phase(got) != "Done" {
		t.Fatalf("got %v, %v", Phase(got), err)
	}
}

func TestUntilConditionErrorStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	boom := &FailedError{Name: "b1", Phase: "Broken", Reason: "x"}
	_, err := untilCondition(ctx, &staticLW{obj: backup("b1", "Broken", "")}, "b1",
		func(*unstructured.Unstructured) (bool, error) { return false, boom })
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("want *FailedError, got %v", err)
	}
}

func TestUntilConditionTimeoutReturnsLastObject(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err := untilCondition(ctx, &staticLW{obj: backup("b1", "Working", "")}, "b1", phaseIs("Done"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if Phase(got) != "Working" {
		t.Fatalf("want last object with phase Working, got %v", got)
	}
}

func TestForConditionValidates(t *testing.T) {
	if _, err := ForCondition(context.Background(), nil, k8s.BackupGVR, "", phaseIs("x")); err == nil {
		t.Fatal("empty name should fail")
	}
	if _, err := ForCondition(context.Background(), nil, k8s.BackupGVR, "b1", nil); err == nil {
		t.Fatal("nil condition should fail")
	}
}
