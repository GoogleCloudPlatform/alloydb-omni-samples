package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// Waiting for a DBCluster to reach a phase, shared by create, stop and start.

const (
	// dbClusterPodLabel is on every pod the operator creates for a DBCluster,
	// and carries the cluster's name.
	dbClusterPodLabel = "alloydbomni.internal.dbadmin.goog/dbcluster"

	// unschedulableStrikes is how many consecutive polls must agree before an
	// unschedulable pod is treated as fatal. One sighting is normal.
	unschedulableStrikes = 3

	// incidentStrikes is the same idea for criticalIncidents. Omni posts
	// transient ones -- a stop/start cycle can briefly show
	// "[DBSE0001] Internal. General Controller Error." and then recover -- so
	// a single sighting must not end the wait.
	incidentStrikes = 2
)

// incidentPollInterval is how often a wait re-reads the cluster looking for a
// failure. Polling is necessary here: criticalIncidents does not move
// status.phase, so a phase watch never sees it. A variable so tests do not sleep.
var incidentPollInterval = 10 * time.Second

// rollbackTimeout bounds the secret cleanup that runs after a failed create.
// A variable so tests do not wait on it.
var rollbackTimeout = 10 * time.Second

// waitForClusterPhase blocks until the cluster's status.phase reaches
// wantPhase. Shared by create, stop and start.
//
// Omni has no single "failed" phase for a DBCluster, so wait.Options.Failure
// has nothing to match on and a broken cluster would otherwise sit here until
// --timeout expires. status.criticalIncidents is where the operator records
// the real failure, so watch that alongside the phase and give up as soon as
// one appears.
func waitForClusterPhase(ctx context.Context, cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name, wantPhase string) error {
	waitCtx, cancel := opts.WithTimeout(ctx)
	defer cancel()

	// Cancelling waitCtx is what breaks ForPhaseWatch out early.
	//
	// The goroutine has to be joined rather than abandoned: it reads package
	// state that tests replace around a call, so letting it outlive the wait
	// is a data race -- `go test -race` reports one -- and it would go on
	// polling the API server after the command had moved on.
	problems := make(chan error, 1)
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		watchForProblems(waitCtx, cancel, c, name, problems)
	}()
	defer func() {
		cancel()
		watcher.Wait()
	}()

	done, err := wait.ForPhaseWatch(waitCtx, c, wait.Options{
		GVR:     k8s.DBClusterGVR,
		Name:    name,
		Success: []string{wantPhase}, //nolint:gocritic // one phase per call
		Timeout: 0,                   // deadline is already on waitCtx
		OnChange: func(phase string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s\n", name, phase)
		},
	})
	if err != nil {
		// The watcher cancels the wait, so its reason outranks the generic
		// "context canceled" that ForPhaseWatch reports back.
		select {
		case problem := <-problems:
			return problem
		default:
		}

		var failed *wait.FailedError
		switch {
		case errors.As(err, &failed):
			return fmt.Errorf("cluster %s failed: %s", failed.Name, failed.Reason)
		case errors.Is(err, context.DeadlineExceeded):
			// Fallback: the watcher above normally catches these first, but its
			// reads can fail, so check the object the phase watch last saw.
			if msgs := criticalIncidentMessages(done); len(msgs) > 0 {
				return fmt.Errorf("cluster %q did not reach %s in time; the operator reports:\n  - %s",
					name, wantPhase, strings.Join(msgs, "\n  - "))
			}
			return fmt.Errorf("cluster %q did not reach %s in time (last phase %s); check `adbo status %s`: %w",
				name, wantPhase, orUnknownPhase(wait.Phase(done)), name, err)
		default:
			return fmt.Errorf("waiting for cluster %s: %w", name, c.FormatErr(err))
		}
	}
	fmt.Fprintf(opts.Prose(cmd), "Cluster %q is %s.\n", done.GetName(), wait.Phase(done))
	return nil
}

// watchForProblems ends the wait early for the two failures that otherwise
// burn the whole --timeout: the operator recording a critical incident, and
// the instance pod never being scheduled. It stops on its own when the wait
// context ends, so a healthy cluster costs a few extra reads and nothing more.
func watchForProblems(ctx context.Context, cancel context.CancelFunc, c *k8s.Clients, name string, found chan<- error) {
	// A pod is Pending for a moment during every normal create, so only treat
	// it as fatal once the scheduler has kept saying no.
	unschedulable, incidents, polls := 0, 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(incidentPollInterval):
		}

		db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue // transient read failure; the phase watch owns real errors
		}
		if msgs := criticalIncidentMessages(db); len(msgs) > 0 {
			incidents++
			if incidents >= incidentStrikes {
				found <- fmt.Errorf("cluster %q reported critical incidents while waiting:\n  - %s",
					name, strings.Join(msgs, "\n  - "))
				cancel()
				return
			}
		} else {
			incidents = 0
		}

		polls++
		if polls == 1 {
			continue // the instance pod does not exist yet; nothing to judge
		}

		switch msg := schedulingFailure(ctx, c, name); msg {
		case "":
			unschedulable = 0
		default:
			unschedulable++
			if unschedulable >= unschedulableStrikes {
				found <- fmt.Errorf("cluster %q has a pod that cannot be scheduled: %s\n"+
					"Free capacity on the cluster, or use a smaller --cpu/--memory/--disk", name, msg)
				cancel()
				return
			}
		}
	}
}

// schedulingFailure returns the scheduler's own explanation when an instance
// pod for this cluster is stuck Pending.
//
// Pods are selected by the operator's own dbcluster label rather than by name.
// Matching names would mean assuming the al-<hash>-<cluster>-<ordinal> scheme
// and would confuse clusters whose names are prefixes of each other, since
// "my-db-2"'s pod name contains "-my-db-" too.
func schedulingFailure(ctx context.Context, c *k8s.Clients, cluster string) string {
	pods, err := c.Typed.CoreV1().Pods(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: dbClusterPodLabel + "=" + cluster,
	})
	if err != nil {
		return ""
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodPending {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Message != "" {
				return cond.Message
			}
		}
	}
	return ""
}

// criticalIncidentMessages renders status.criticalIncidents as "[code] message".
// db may be nil: the waiter returns no object if it never observed one.
func criticalIncidentMessages(db *unstructured.Unstructured) []string {
	if db == nil {
		return nil
	}
	incidents, _, _ := unstructured.NestedSlice(db.Object, "status", "criticalIncidents")
	var msgs []string
	for _, it := range incidents {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		msg, _ := m["message"].(string)
		if code, ok := m["code"].(string); ok && code != "" {
			msg = "[" + code + "] " + msg
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func orUnknownPhase(phase string) string {
	if phase == "" {
		return "unknown (the operator has not set status.phase yet)"
	}
	return strconv.Quote(phase)
}

// rollbackSecret removes the secret this command created, after the DBCluster
// create failed. An orphan secret blocks the user's own retry when they pass
// --password, so if it cannot be removed, say exactly how to remove it.
func rollbackSecret(ctx context.Context, cmd *cobra.Command, c *k8s.Clients, secretName string) {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()

	if err := deleteSecretWithRetry(ctx, c, secretName); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: the cluster was not created and the secret %q could not be removed: %v\n"+
				"Remove it before retrying with --password: kubectl delete secret %s -n %s\n",
			secretName, err, secretName, c.Namespace)
	}
}

// cleanupContext returns a context fit for undoing work after a command has
// already failed. One common reason for that failure is a Ctrl+C, which leaves
// the command's context cancelled: cleaning up on it would fail instantly and
// strand the secret. So detach from the cancellation, but keep a deadline, or
// a wedged API server would hang the command on its way out.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
}

// deleteSecretWithRetry tries twice, since the usual cause of failure here is
// a transient API error.
func deleteSecretWithRetry(ctx context.Context, c *k8s.Clients, secretName string) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.Typed.CoreV1().Secrets(c.Namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
	return err
}
