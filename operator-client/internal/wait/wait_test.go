package wait

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// staticLW is a ListerWatcher that always Lists obj. Watch stays open
// and never emits, so timeout tests can expire the context.
type staticLW struct {
	obj *unstructured.Unstructured
}

func (s *staticLW) IsWatchListSemanticsUnSupported() bool { return true }

func (s *staticLW) List(metav1.ListOptions) (runtime.Object, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(k8s.BackupGVR.GroupVersion().WithKind("BackupList"))
	list.SetResourceVersion("1")
	if s.obj != nil {
		obj := s.obj.DeepCopy()
		obj.SetResourceVersion("1")
		list.Items = []unstructured.Unstructured{*obj}
	}
	return list, nil
}

func (s *staticLW) Watch(metav1.ListOptions) (watch.Interface, error) {
	return watch.NewFake(), nil
}

func backup(name, phase, message string) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": "adbo-eugene"},
	}
	status := map[string]any{}
	if phase != "" {
		status["phase"] = phase
	}
	if message != "" {
		status["message"] = message
	}
	if len(status) > 0 {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestPhaseNil(t *testing.T) {
	if Phase(nil) != "" {
		t.Fatal("nil object should have empty phase")
	}
}

func TestWatchUntilAlreadySucceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := watchUntil(ctx, &staticLW{obj: backup("b1", "Succeeded", "")}, Options{
		Name:    "b1",
		Success: []string{"Succeeded"},
		Failure: []string{"Failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetName() != "b1" || Phase(got) != "Succeeded" {
		t.Fatalf("got %s %s", got.GetName(), Phase(got))
	}
}

func TestWatchUntilFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := watchUntil(ctx, &staticLW{obj: backup("b1", "Failed", "disk full")}, Options{
		Name:    "b1",
		Success: []string{"Succeeded"},
		Failure: []string{"Failed"},
	})
	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("got %v", err)
	}
	if failed.Reason != "disk full" {
		t.Fatalf("reason = %q", failed.Reason)
	}
}

func TestWatchUntilTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := watchUntil(ctx, &staticLW{obj: backup("b1", "Running", "")}, Options{
		Name:    "b1",
		Success: []string{"Succeeded"},
		Failure: []string{"Failed"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), `last phase "Running"`) {
		t.Fatalf("got %v", err)
	}
}

func TestWatchUntilOnChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var saw []string
	_, err := watchUntil(ctx, &staticLW{obj: backup("b1", "Succeeded", "")}, Options{
		Name:    "b1",
		Success: []string{"Succeeded"},
		OnChange: func(phase string) {
			saw = append(saw, phase)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saw) != 1 || saw[0] != "Succeeded" {
		t.Fatalf("OnChange = %v", saw)
	}
}

func TestForPhaseWatchRequiresName(t *testing.T) {
	_, err := ForPhaseWatch(context.Background(), nil, Options{Success: []string{"Succeeded"}})
	if err == nil {
		t.Fatal("expected an error")
	}
}
