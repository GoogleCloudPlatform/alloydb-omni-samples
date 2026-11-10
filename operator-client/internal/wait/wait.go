// Package wait blocks until a Custom Resource reaches a success or failure
// phase. AlloyDB Omni operations (backup, restore, failover, create) are
// asynchronous: Create returns immediately and the operator updates
// status.phase later.
//
// Commands that take --wait should call ForPhaseWatch instead of polling
// with Sleep+Get. This Lists first, then Watches (same idea as
// `kubectl wait --for=jsonpath='{.status.phase}'=Succeeded`), so a phase
// that is already terminal when we start is not missed.
//
// Do not use this package for deletes. A Deleted event is treated as an
// error; waiting until an object is gone needs a different helper.
package wait

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// Options is the input to ForPhaseWatch.
type Options struct {
	GVR      schema.GroupVersionResource // e.g. k8s.BackupGVR
	Name     string                      // object name in the current namespace
	Success  []string                    // phases that mean done, e.g. {"Succeeded"}
	Failure  []string                    // phases that mean failed, e.g. {"Failed"}
	Timeout  time.Duration               // 0 means use only the parent context deadline
	OnChange func(phase string)          // optional; called when phase changes to a non-empty value
}

// FailedError is returned when the object enters a Failure phase.
// Callers distinguish it with errors.As.
type FailedError struct {
	Name, Phase, Reason string
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("%s ended in phase %s: %s", e.Name, e.Phase, e.Reason)
}

// Phase reads status.phase. Empty if obj is nil or the operator has not written it yet.
func Phase(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	p, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	return p
}

// Reason reads a human-readable failure explanation. Omni often hides the
// real cause in status.message or conditions rather than the default view.
func Reason(obj *unstructured.Unstructured) string {
	if obj == nil {
		return "no reason reported; run kubectl describe for details"
	}
	if msg, ok, _ := unstructured.NestedString(obj.Object, "status", "message"); ok && msg != "" {
		return msg
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if msg, _ := m["message"].(string); msg != "" {
			return msg
		}
	}
	return "no reason reported; run kubectl describe for details"
}

// ForPhaseWatch watches one object until its status.phase is in Success
// or Failure.
func ForPhaseWatch(ctx context.Context, c *k8s.Clients, o Options) (*unstructured.Unstructured, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	if c == nil {
		return nil, fmt.Errorf("wait: clients are required")
	}
	return watchUntil(ctx, listWatchOne(ctx, c, o.GVR, o.Name), o)
}

func watchUntil(ctx context.Context, lw cache.ListerWatcher, o Options) (*unstructured.Unstructured, error) {
	var last *unstructured.Unstructured
	var prevPhase string
	check := func(obj *unstructured.Unstructured) (bool, error) {
		last = obj
		phase := Phase(obj)
		if phase != prevPhase {
			if o.OnChange != nil && phase != "" {
				o.OnChange(phase)
			}
			prevPhase = phase
		}
		switch {
		case slices.Contains(o.Success, phase):
			return true, nil
		case slices.Contains(o.Failure, phase):
			return false, &FailedError{Name: o.Name, Phase: phase, Reason: Reason(obj)}
		}
		return false, nil
	}

	// Precondition runs after the informer Lists, so an object that is
	// already Succeeded/Failed is not missed (nil precondition only sees
	// later watch events).
	_, err := watchtools.UntilWithSync(ctx, lw, &unstructured.Unstructured{},
		func(store cache.Store) (bool, error) {
			for _, item := range store.List() {
				obj, ok := item.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				done, err := check(obj)
				if done || err != nil {
					return done, err
				}
			}
			return false, nil
		},
		func(ev watch.Event) (bool, error) {
			obj, err := eventObject(ev, o.Name)
			if err != nil || obj == nil {
				return false, err
			}
			return check(obj)
		})
	return last, wrapDeadline(err, ctx, o, last)
}

func (o Options) validate() error {
	if o.Name == "" {
		return fmt.Errorf("wait: object name is required")
	}
	if len(o.Success) == 0 {
		return fmt.Errorf("wait: at least one success phase is required")
	}
	return nil
}

// namedListWatch is a List+Watch of one object. IsWatchListSemanticsUnSupported
// keeps client-go on classic List then Watch; WatchList (default in 0.37) waits
// for a bookmark that fake clients never send.
type namedListWatch struct{ *cache.ListWatch }

func (namedListWatch) IsWatchListSemanticsUnSupported() bool { return true }

func listWatchOne(ctx context.Context, c *k8s.Clients, gvr schema.GroupVersionResource, name string) cache.ListerWatcher {
	sel := fields.OneTermEqualSelector("metadata.name", name).String()
	return namedListWatch{ListWatch: &cache.ListWatch{
		ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
			opts.FieldSelector = sel
			return c.In(gvr).List(ctx, opts)
		},
		WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
			opts.FieldSelector = sel
			return c.In(gvr).Watch(ctx, opts)
		},
	}}
}

// eventObject returns the CR, or an error on Deleted. Bookmark / unknown types yield (nil, nil).
func eventObject(ev watch.Event, name string) (*unstructured.Unstructured, error) {
	if ev.Type == watch.Deleted {
		return nil, fmt.Errorf("%s was deleted while waiting", name)
	}
	obj, ok := ev.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, nil
	}
	return obj, nil
}

func wrapDeadline(err error, ctx context.Context, o Options, last *unstructured.Unstructured) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("timed out waiting for %s to reach %s (last phase %q): %w",
		o.Name, strings.Join(o.Success, " or "), Phase(last), context.DeadlineExceeded)
}
