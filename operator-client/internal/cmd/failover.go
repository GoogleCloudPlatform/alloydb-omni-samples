package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

type haFlags struct {
	newPrimary string
	wait       bool
}

type haKind struct {
	kind    string
	verb    string
	warning string
}

var (
	failoverKind = haKind{
		kind:    "Failover",
		verb:    "fail over",
		warning: "Use this when the primary is down. Recent writes that have not\n" + "reached the standby may be lost, and the old primary is rebuilt.",
	}
	switchoverKind = haKind{
		kind:    "Switchover",
		verb:    "switch over",
		warning: "The primary and standby swap roles with no data loss.\n" + "Writes pause briefly while they switch.",
	}
)

func newFailoverCommand(opts *GlobalOptions) *cobra.Command {
	return newHACommand(opts, failoverKind,
		"failover [CLUSTER_NAME]",
		"Promote a standby when the primary is down (may lose recent writes)",
		"  adbo failover my-db\n"+
			"  adbo failover my-db --to my-db-instance-b -y")
}

func newSwitchoverCommand(opts *GlobalOptions) *cobra.Command {
	return newHACommand(opts, switchoverKind,
		"switchover [CLUSTER_NAME]",
		"Swap the primary with a standby, with no data loss",
		"  adbo switchover my-db\n"+
			"  adbo switchover my-db --to my-db-instance-b --no-wait")
}

func newHACommand(opts *GlobalOptions, k haKind, use, short, example string) *cobra.Command {
	var f haFlags
	var waitFlags *WaitFlags

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ".\n\n" + k.warning + "\n" +
			"adbo asks before starting (skip with -y). By default it waits until the\n" +
			"operation finishes and prints the old and new primary; --no-wait returns at once.",
		Example: example,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runHAOperation(cmd, opts, k, name, f)
		},
	}

	cmd.Flags().StringVar(&f.newPrimary, "to", "",
		"standby instance to promote (default: the only standby, or the operator's choice)")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd
}

func (k haKind) gvr() schema.GroupVersionResource {
	if k.kind == "Switchover" {
		return k8s.SwitchoverGVR
	}
	return k8s.FailoverGVR
}

func newHAOperation(k haKind, namespace, cluster, newPrimary string) *unstructured.Unstructured {
	spec := map[string]any{
		"dbclusterRef": cluster,
	}

	if newPrimary != "" {
		spec["newPrimary"] = newPrimary
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       k.kind,
		"metadata": map[string]any{
			"generateName": cluster + "-" + strings.ToLower(k.kind) + "-",
			"namespace":    namespace,
		},
		"spec": spec,
	}}
}

const (
	instanceClusterLabel = "alloydbomni.internal.dbadmin.goog/dbcluster"
	instanceRoleLabel    = "dbs.internal.dbadmin.goog/ha-role"
)

func clusterInstances(ctx context.Context, c *k8s.Clients, cluster string) (primary string, standbys []string,
	err error) {
	list, err := c.In(k8s.InstanceGVR).List(ctx, metav1.ListOptions{
		LabelSelector: instanceClusterLabel + "=" + cluster,
	})
	if err != nil {
		return "", nil, c.FormatErr(err)
	}
	for _, inst := range list.Items {
		switch inst.GetLabels()[instanceRoleLabel] {
		case "Primary":
			primary = inst.GetName()
		case "Standby":
			standbys = append(standbys, inst.GetName())
		}
	}
	return primary, standbys, nil
}

func pickNewPrimary(k haKind, cluster, requested string, standbys []string) (string, error) {
	if len(standbys) == 0 {
		return "", fmt.Errorf("cluster %q has no standby to %s to; add one with `adbo cluster scale %s --standbys 1`",
			cluster, k.verb, cluster)
	}
	if requested != "" {
		if !slices.Contains(standbys, requested) {
			return "", fmt.Errorf("%q is not a standby of cluster %q; standbys: %s",
				requested, cluster, strings.Join(standbys, ", "))
		}
		return requested, nil
	}
	if len(standbys) == 1 {
		return standbys[0], nil
	}
	if k.kind == "Failover" {
		return "", nil
	}
	return "", fmt.Errorf("cluster %q has %d standbys; choose one with --to (%s)",
		cluster, len(standbys), strings.Join(standbys, ", "))
}

func runHAOperation(cmd *cobra.Command, opts *GlobalOptions, k haKind, cluster string, f haFlags) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	if _, err := c.In(k8s.DBClusterGVR).Get(ctx, cluster, metav1.GetOptions{}); err != nil {
		return c.FormatErr(err)
	}

	oldPrimary, standbys, err := clusterInstances(ctx, c, cluster)
	if err != nil {
		return err
	}
	newPrimary, err := pickNewPrimary(k, cluster, f.newPrimary, standbys)
	if err != nil {
		return err
	}

	target := newPrimary
	if target == "" {
		target = "a standby chosen by the operator"
	}
	prompt := fmt.Sprintf("This will %s cluster %q%s: primary %s -> %s.\n%s\nContinue?",
		k.verb, cluster, k8s.Location(c.Namespace, c.Context), oldPrimary, target, k.warning)
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return errDeclined
	}

	created, err := c.In(k.gvr()).Create(ctx,
		newHAOperation(k, c.Namespace, cluster, newPrimary), metav1.CreateOptions{})
	if err != nil {
		return c.FormatErr(err)
	}

	result := created
	if f.wait {
		result, err = waitForHAOperation(cmd, opts, c, k, created.GetName())
		if err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, result.Object)
	}
	lower := strings.ToLower(k.kind)
	if !f.wait {
		fmt.Fprintf(out, "Started %s %q of cluster %q. Check progress with `kubectl get %s %s -n %s`.\n",
			lower, created.GetName(), cluster, lower, created.GetName(), c.Namespace)
		return nil
	}
	from := k8s.StringField(result, "status", "internal", "oldPrimary")
	to := k8s.StringField(result, "status", "internal", "newPrimary")
	fmt.Fprintf(out, "%s of cluster %q complete: primary %s -> %s\n", k.kind, cluster, from, to)
	return nil
}

func haState(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	s, _, _ := unstructured.NestedString(obj.Object, "status", "state")
	return s
}

func haOperationDone(obj *unstructured.Unstructured) (bool, error) {
	state := haState(obj)
	switch {
	case state == "Success":
		return true, nil
	case state == "Failed_RollbackSuccess":
		return false, &wait.FailedError{Name: obj.GetName(), Phase: state,
			Reason: "it failed but was rolled back; the cluster is unchanged. " + wait.Reason(obj)}
	case strings.HasPrefix(state, "Failed"):
		return false, &wait.FailedError{Name: obj.GetName(), Phase: state, Reason: wait.Reason(obj)}
	}
	return false, nil
}

func waitForHAOperation(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, k haKind, name string) (*unstructured.Unstructured, error) {
	errOut := cmd.ErrOrStderr()
	fmt.Fprintf(errOut, "Waiting for %s %q to finish...\n", strings.ToLower(k.kind), name)

	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()

	prev := ""
	last, err := wait.ForCondition(ctx, c, k.gvr(), name, func(obj *unstructured.Unstructured) (bool, error) {
		if s := haState(obj); s != "" && s != prev {
			fmt.Fprintf(errOut, "  %s: %s\n", name, s)
			prev = s
		}
		return haOperationDone(obj)
	})

	var failed *wait.FailedError
	switch {
	case err == nil:
		return last, nil
	case errors.As(err, &failed):
		return nil, fmt.Errorf("%s %s failed (%s): %s", strings.ToLower(k.kind), failed.Name, failed.Phase, failed.Reason)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("%s %q did not finish within %s (last state %q); it is still running, "+
			"check `kubectl get %s %s -n %s`: %w",
			strings.ToLower(k.kind), name, opts.Timeout, haState(last),
			strings.ToLower(k.kind), name, c.Namespace, err)
	default:
		return nil, c.FormatErr(err)
	}
}
