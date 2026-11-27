package wait

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// Condition reports whether obj has reached the wanted state. A non-nil
// error stops the wait and is returned unchanged (use *FailedError for a
// terminal failure).
type Condition func(obj *unstructured.Unstructured) (done bool, err error)

// ForCondition watches one object, List then Watch like ForPhaseWatch,
// until cond reports done. Use it when "done" depends on more than
// status.phase: after a spec change to an object that is already Ready,
// for example, the phase alone would match before the operator reacts.
//
// The deadline comes from ctx (see GlobalOptions.WithTimeout). On timeout
// the error wraps context.DeadlineExceeded. The last object seen is
// returned in every case, or nil when none was seen.
func ForCondition(ctx context.Context, c *k8s.Clients, gvr schema.GroupVersionResource, name string, cond Condition) (*unstructured.Unstructured, error) {
	if name == "" {
		return nil, fmt.Errorf("wait: object name is required")
	}
	if cond == nil {
		return nil, fmt.Errorf("wait: a condition is required")
	}
	if c == nil {
		return nil, fmt.Errorf("wait: clients are required")
	}
	return untilCondition(ctx, listWatchOne(ctx, c, gvr, name), name, cond)
}

func untilCondition(ctx context.Context, lw cache.ListerWatcher, name string, cond Condition) (*unstructured.Unstructured, error) {
	var last *unstructured.Unstructured
	check := func(obj *unstructured.Unstructured) (bool, error) {
		last = obj
		return cond(obj)
	}
	_, err := watchtools.UntilWithSync(ctx, lw, &unstructured.Unstructured{},
		// Precondition sees the Listed object, so an object that already
		// satisfies cond is not missed.
		func(store cache.Store) (bool, error) {
			for _, item := range store.List() {
				if obj, ok := item.(*unstructured.Unstructured); ok {
					if done, err := check(obj); done || err != nil {
						return done, err
					}
				}
			}
			return false, nil
		},
		func(ev watch.Event) (bool, error) {
			obj, err := eventObject(ev, name)
			if err != nil || obj == nil {
				return false, err
			}
			return check(obj)
		})
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return last, fmt.Errorf("timed out waiting for %s: %w", name, context.DeadlineExceeded)
	}
	return last, err
}
