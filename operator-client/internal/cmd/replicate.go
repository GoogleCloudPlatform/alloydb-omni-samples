package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"strings"
)

type replicateFlags struct {
	sourceContext, sourceNamespace string
	targetContext, targetNamespace string
	wait                           bool
	cleanUpstream                  bool
}

var newSideClients = func(opts *GlobalOptions, kubeContext, namespace string) (*k8s.Clients, error) {
	if kubeContext == "" && namespace == "" {
		return opts.Clients()
	}
	// ConfigFlags holds locks and a cached config, so copy fields one by one
	// instead of copying the struct.
	flags := genericclioptions.NewConfigFlags(true)
	if g := opts.KubeFlags; g != nil {
		// Settings about the user or this machine apply to both sides.
		flags.KubeConfig, flags.CacheDir, flags.Namespace = g.KubeConfig, g.CacheDir, g.Namespace
		flags.Impersonate, flags.ImpersonateUID = g.Impersonate, g.ImpersonateUID
		flags.ImpersonateGroup, flags.ImpersonateUserExtra = g.ImpersonateGroup, g.ImpersonateUserExtra
		flags.Timeout, flags.DisableCompression, flags.ProxyURL = g.Timeout, g.DisableCompression, g.ProxyURL
		// Server and credentials belong to one cluster: keep them only when
		// this side stays on the global context, otherwise the side's own
		// context supplies them.
		if kubeContext == "" {
			flags.Context, flags.ClusterName, flags.AuthInfoName = g.Context, g.ClusterName, g.AuthInfoName
			flags.APIServer, flags.TLSServerName, flags.Insecure = g.APIServer, g.TLSServerName, g.Insecure
			flags.CertFile, flags.KeyFile, flags.CAFile = g.CertFile, g.KeyFile, g.CAFile
			flags.BearerToken, flags.Username, flags.Password = g.BearerToken, g.Username, g.Password
		}
	}
	if kubeContext != "" {
		flags.Context = &kubeContext
	}
	if namespace != "" {
		flags.Namespace = &namespace
	}
	return k8s.New(flags)
}

func newUpstream(namespace, source, target string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Replication",
		"metadata":   map[string]any{"name": target + "-upstream", "namespace": namespace},
		"spec": map[string]any{
			"dbcluster": map[string]any{"name": source},
			"upstream":  map[string]any{},
		},
	}}
}

func newDownstream(namespace, target, secretName string, up *unstructured.Unstructured) *unstructured.Unstructured {
	port, _, _ := unstructured.NestedInt64(up.Object, "status", "upstream", "port")
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Replication",
		"metadata":   map[string]any{"name": target + "-downstream", "namespace": namespace},
		"spec": map[string]any{
			"dbcluster": map[string]any{"name": target},
			"downstream": map[string]any{
				"host":                k8s.StringField(up, "status", "upstream", "host"),
				"port":                port,
				"username":            k8s.StringField(up, "status", "upstream", "username"),
				"password":            map[string]any{"name": secretName},
				"replicationSlotName": k8s.StringField(up, "status", "upstream", "replicationSlotName"),
				"control":             "setup",
			},
		},
	}}
}

func upstreamReady(up *unstructured.Unstructured) (bool, error) {
	if k8s.ConditionStatus(up, "Ready") != "True" {
		return false, nil
	}
	for _, f := range []string{"host", "username", "replicationSlotName"} {
		if k8s.StringField(up, "status", "upstream", f) == "-" {
			return false, nil
		}

	}
	if k8s.StringField(up, "status", "upstream", "password", "name") == "-" {
		return false, nil
	}
	port, _, _ := unstructured.NestedInt64(up.Object, "status", "upstream", "port")
	return port > 0, nil
}

func copyReplicationSecret(ctx context.Context, src, dst *k8s.Clients, up *unstructured.Unstructured, target string) (string, error) {
	name := k8s.StringField(up, "status", "upstream", "password", "name")
	ns := k8s.StringField(up, "status", "upstream", "password", "namespace")
	if ns == "-" {
		ns = src.Namespace
	}
	secret, err := src.Typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", src.FormatErr(err)
	}
	copyName := target + "-replication-pw"
	secrets := dst.Typed.CoreV1().Secrets(dst.Namespace)
	_, err = secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: copyName, Namespace: dst.Namespace},
		Data:       secret.Data,
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Left from an earlier run; refresh it in case the password changed.
		existing, getErr := secrets.Get(ctx, copyName, metav1.GetOptions{})
		if getErr != nil {
			return "", dst.FormatErr(getErr)
		}
		existing.Data = secret.Data
		_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", dst.FormatErr(err)
	}
	return copyName, nil
}

func downstreamReady(down *unstructured.Unstructured) (bool, error) {
	return k8s.ConditionStatus(down, "Ready") == "True" && k8s.ConditionStatus(down, "Healthy") == "True", nil
}

func runReplicateCreate(cmd *cobra.Command, opts *GlobalOptions, source, target string, f replicateFlags) error {
	ctx := cmd.Context()
	src, err := newSideClients(opts, f.sourceContext, f.sourceNamespace)
	if err != nil {
		return err
	}
	dst, err := newSideClients(opts, f.targetContext, f.targetNamespace)
	if err != nil {
		return err
	}
	if source == target && src.Context == dst.Context && src.Namespace == dst.Namespace {
		return fmt.Errorf("source and target are the same cluster %q: a cluster cannot replicate to itself", source)
	}
	if _, err := src.In(k8s.DBClusterGVR).Get(ctx, source, metav1.GetOptions{}); err != nil {
		return src.FormatErr(err)
	}
	if _, err := dst.In(k8s.DBClusterGVR).Get(ctx, target, metav1.GetOptions{}); err != nil {
		return dst.FormatErr(err)
	}
	prompt := fmt.Sprintf("Cluster %q%s will be replaced by a live copy of %q%s.\n"+"ALL data now in %q will be lost. Continue?", target, k8s.Location(dst.Namespace, dst.Context), source, k8s.Location(src.Namespace, src.Context), target)
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return errDeclined
	}
	waitCtx, cancel := opts.WithTimeout(ctx)
	defer cancel()

	up := newUpstream(src.Namespace, source, target)
	if _, err := src.In(k8s.ReplicationGVR).Create(ctx, up, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return src.FormatErr(err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for %q to be ready to share...\n", source)
	up, err = wait.ForCondition(waitCtx, src, k8s.ReplicationGVR, up.GetName(), upstreamReady)

	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("source side was not ready within %s; check `kubectl get replication %s`: %w", opts.Timeout, up.GetName(), err)
	}
	if err != nil {
		return src.FormatErr(err)
	}

	secretName, err := copyReplicationSecret(ctx, src, dst, up, target)
	if err != nil {
		return err
	}
	down := newDownstream(dst.Namespace, target, secretName, up)
	created, err := dst.In(k8s.ReplicationGVR).Create(ctx, down, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = dst.In(k8s.ReplicationGVR).Get(ctx, down.GetName(), metav1.GetOptions{})
	}
	if err != nil {
		return dst.FormatErr(err)
	}
	out := cmd.OutOrStdout()
	if !f.wait {
		fmt.Fprintf(out, "Replicating %q to %q. Check progress with `adbo replicate status %s`.\n", source, target, target)
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for %q to catch up...\n", target)
	if _, err := wait.ForCondition(waitCtx, dst, k8s.ReplicationGVR, created.GetName(), downstreamReady); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("target is set up but not streaming yet after %s; check `adbo replicate status %s`: %w", opts.Timeout, target, err)
		}
		return dst.FormatErr(err)
	}
	fmt.Fprintf(out, "Replicating %q to %q.\n", source, target)
	return nil
}

func newReplicateCommand(opts *GlobalOptions) *cobra.Command {
	var f replicateFlags
	c := &cobra.Command{
		Use:   "replicate",
		Short: "Keep a live copy of a cluster in another place",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	c.PersistentFlags().StringVar(&f.sourceContext, "source-context",
		"", "kube context of the source (default: --context)")
	c.PersistentFlags().StringVar(&f.sourceNamespace,
		"source-namespace", "", "namespace of the source (default: -n)")
	c.PersistentFlags().StringVar(&f.targetContext, "target-context",
		"", "kube context of the target (default: --context)")
	c.PersistentFlags().StringVar(&f.targetNamespace,
		"target-namespace", "", "namespace of the target (default: -n)")
	c.AddCommand(newReplicateCreateCommand(opts, &f))
	c.AddCommand(newReplicateStatusCommand(opts, &f))
	c.AddCommand(newReplicatePromoteCommand(opts, &f))
	c.AddCommand(newReplicateDeleteCommand(opts, &f))
	return c
}
func newReplicateCreateCommand(opts *GlobalOptions, f *replicateFlags) *cobra.Command {
	var waitFlags *WaitFlags
	cmd := &cobra.Command{
		Use:   "create SOURCE TARGET",
		Short: "Start copying SOURCE into TARGET (replaces TARGET's data)",
		Long: "Make TARGET a live, read-only copy of SOURCE. Everything in TARGET is\n" +
			"replaced, so adbo asks first (-y skips). SOURCE and TARGET can be in\n" +
			"different namespaces or Kubernetes clusters; see --source-context and\n" +
			"--target-context.",
		Example: "  adbo replicate create my-db my-db-copy\n" +
			"  adbo replicate create my-db my-db-dr --source-context us --target-context eu",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			return runReplicateCreate(cmd, opts, args[0],
				args[1], *f)
		},
	}
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd

}

func replicationRow(side, name string, c *k8s.Clients, obj *unstructured.Unstructured) []string {
	if obj == nil {
		return []string{side, name, c.Context, c.Namespace, "-", "-", "not found"}
	}
	problem := "-"
	if k8s.ConditionStatus(obj, "Ready") != "True" || (side == "downstream" && k8s.ConditionStatus(obj, "Healthy") != "True") {
		problem = wait.Reason(obj)
	}
	return []string{side, name, c.Context, c.Namespace, k8s.ConditionStatus(obj, "Ready"), k8s.ConditionStatus(obj, "Healthy"), problem}
}

func findReplication(ctx context.Context, c *k8s.Clients, name string) (*unstructured.Unstructured, error) {
	obj, err := c.In(k8s.ReplicationGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, c.FormatErr(err)
	}
	return obj, nil
}

func runReplicateStatus(cmd *cobra.Command, opts *GlobalOptions, target string, f replicateFlags) error {
	ctx := cmd.Context()
	src, err := newSideClients(opts, f.sourceContext, f.sourceNamespace)
	if err != nil {
		return err
	}
	dst, err := newSideClients(opts, f.targetContext, f.targetNamespace)
	if err != nil {
		return err
	}
	upName, downName := target+"-upstream", target+"-downstream"
	up, err := findReplication(ctx, src, upName)
	if err != nil {
		return err
	}
	down, err := findReplication(ctx, dst, downName)
	if err != nil {
		return err
	}
	if up == nil && down == nil {
		return fmt.Errorf("no replication found for %q: looked for %s%s and %s%s", target, upName, k8s.Location(src.Namespace, src.Context), downName, k8s.Location(dst.Namespace, dst.Context))
	}
	table := output.Table{
		Headers: []string{"SIDE", "NAME", "CONTEXT", "NAMESPACE", "READY", "HEALTHY", "PROBLEM"},
		Rows: [][]string{
			replicationRow("upstream", upName, src, up),
			replicationRow("downstream", downName, dst, down),
		},
	}
	var raw []any
	for _, obj := range []*unstructured.Unstructured{up, down} {
		if obj != nil {
			raw = append(raw, obj.Object)
		}
	}
	return output.Print(cmd.OutOrStdout(), opts.Output, table, raw)
}

func newReplicateStatusCommand(opts *GlobalOptions, f *replicateFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status TARGET",
		Short: "Show whether replication into TARGET is working",
		Example: "  adbo replicate status my-db-copy\n" +
			"  adbo replicate status my-db-dr --source-context us --target-context eu",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReplicateStatus(cmd, opts, args[0], *f)
		},
	}
}

func runReplicatePromote(cmd *cobra.Command, opts *GlobalOptions, target string, f replicateFlags) error {
	ctx := cmd.Context()
	dst, err := newSideClients(opts, f.targetContext, f.targetNamespace)
	if err != nil {
		return err
	}
	downName := target + "-downstream"
	down, err := findReplication(ctx, dst, downName)
	if err != nil {
		return err
	}
	if down == nil {
		return fmt.Errorf("no replication into %q found: %s does not exist%s", target, downName, k8s.Location(dst.Namespace, dst.Context))
	}
	out := cmd.OutOrStdout()
	if k8s.StringField(down, "spec", "downstream", "control") == "promote" {
		fmt.Fprintf(out, "Cluster %q is already promoted, nothing to do.\n", target)
		return nil
	}
	prompt := fmt.Sprintf("Promote %q%s: it stops copying from the source (%s) and becomes a normal, writeable cluster.\n"+"Anything written to the source that has not arrived yet will not be copied. Continue?", target, k8s.Location(dst.Namespace, dst.Context), k8s.StringField(down, "spec", "downstream", "host"))
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return errDeclined
	}
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": down.GetResourceVersion()},
		"spec":     map[string]any{"downstream": map[string]any{"control": "promote"}},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	promoted, err := dst.In(k8s.ReplicationGVR).Patch(ctx, downName, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return dst.FormatErr(err)
	}
	// Removing the source side before the promote finishes could cut the
	// target off mid-promote, so --cleanup-upstream always waits.
	waited := f.wait || f.cleanUpstream
	if waited {
		waitCtx, cancel := opts.WithTimeout(ctx)
		defer cancel()
		fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for %q to finish promoting...\n", target)
		if _, err := wait.ForCondition(waitCtx, dst, k8s.ReplicationGVR, downName, promoteDone(promoted.GetGeneration())); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("promote of %q is applied but not finished after %s; check `adbo replicate status %s`: %w", target, opts.Timeout, target, err)
			}
			return dst.FormatErr(err)
		}
	}
	if f.cleanUpstream {
		src, err := newSideClients(opts, f.sourceContext, f.sourceNamespace)
		if err != nil {
			return err
		}
		err = src.In(k8s.ReplicationGVR).Delete(ctx, target+"-upstream", metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("promoted %q, but could not remove the source side: %w", target, src.FormatErr(err))
		}
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: the source still keeps changes for %q, which can fill its disk.\n"+
			"Run `adbo replicate delete %s` (or promote with --cleanup-upstream) once you no longer need it.\n", target, target)
	}
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, promoted.Object)
	}
	verb := "Promoting"
	if waited {
		verb = "Promoted"
	}
	fmt.Fprintf(out, "%s %q%s; it is now a separate, writeable cluster.\n", verb, target, k8s.Location(dst.Namespace, dst.Context))
	return nil

}

func promoteDone(generation int64) wait.Condition {
	return func(down *unstructured.Unstructured) (bool, error) {
		og, _, _ := unstructured.NestedInt64(down.Object, "status", "observedGeneration")
		return og >= generation && k8s.ConditionStatus(down, "Ready") == "True", nil
	}
}

func newReplicatePromoteCommand(opts *GlobalOptions, f *replicateFlags) *cobra.Command {
	var waitFlags *WaitFlags
	cmd := &cobra.Command{
		Use:   "promote TARGET",
		Short: "Stop copying into TARGET and make it a normal, writeable cluster",
		Long: "Cut TARGET loose from its source. adbo asks first (-y skips). Afterwards the\n" + "source still keeps changes for TARGET until its side is removed, which can\n" +
			"fill its disk: pass --cleanup-upstream, or run `adbo replicate delete` later.",
		Example: "  adbo replicate promote my-db-copy\n" +
			"  adbo replicate promote my-db-dr --cleanup-upstream --source-context us --target-context eu",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			return runReplicatePromote(cmd, opts, args[0], *f)
		},
	}
	cmd.Flags().BoolVar(&f.cleanUpstream, "cleanup-upstream", false, "also remove the source side so it stops keeping changes for TARGET")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd

}

func runReplicateDelete(cmd *cobra.Command, opts *GlobalOptions, target string, f replicateFlags) error {
	ctx := cmd.Context()
	src, err := newSideClients(opts, f.sourceContext, f.sourceNamespace)
	if err != nil {
		return err
	}
	dst, err := newSideClients(opts, f.targetContext, f.targetNamespace)
	if err != nil {
		return err
	}
	upName, downName := target+"-upstream", target+"-downstream"
	up, err := findReplication(ctx, src, upName)
	if err != nil {
		return err
	}
	down, err := findReplication(ctx, dst, downName)
	if err != nil {
		return err
	}
	if up == nil && down == nil {
		return fmt.Errorf("no replication found for %q: looked for %s%s and %s%s", target, upName, k8s.Location(src.Namespace, src.Context), downName, k8s.Location(dst.Namespace, dst.Context))
	}
	prompt := fmt.Sprintf("Remove the replication into %q%s? Copying stops; both clusters keep their data.", target, k8s.Location(dst.Namespace, dst.Context))
	if down != nil && k8s.StringField(down, "spec", "downstream", "control") != "promote" {
		prompt += fmt.Sprintf("\n%q has not been promoted, so it stays a read-only copy that no longer updates.", target)
	}
	ok, err := opts.Confirm(cmd, prompt+" Continue?")
	if err != nil {
		return err
	}
	if !ok {
		return errDeclined
	}
	var removed []string
	remove := func(c *k8s.Clients, what, name string, del func() error) error {
		if err := del(); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("removing %s %s: %w", what, name, c.FormatErr(err))
		}
		removed = append(removed, what+" "+name)
		return nil
	}
	if err := remove(dst, "replication", downName, func() error {
		return dst.In(k8s.ReplicationGVR).Delete(ctx, downName, metav1.DeleteOptions{})
	}); err != nil {
		return err
	}
	if err := remove(src, "replication", upName, func() error {
		return src.In(k8s.ReplicationGVR).Delete(ctx, upName, metav1.DeleteOptions{})
	}); err != nil {
		return err
	}
	secretName := target + "-replication-pw"
	if err := remove(dst, "secret", secretName, func() error {
		return dst.Typed.CoreV1().Secrets(dst.Namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Removed the replication into %q (%s).\n", target, strings.Join(removed, ", "))
	return nil
}

func newReplicateDeleteCommand(opts *GlobalOptions, f *replicateFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "delete TARGET",
		Short: "Stop replicating into TARGET and remove both sides (no data is deleted)",
		Long: "Remove the Replication objects on both sides and the password Secret adbo\n" +
			"copied to the target. Both clusters and their data stay. adbo asks first\n" +
			"(-y skips).",
		Example: "  adbo replicate delete my-db-copy\n" +
			"  adbo replicate delete my-db-dr --source-context us --target-context eu -y",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReplicateDelete(cmd, opts, args[0], *f)
		},
	}
}
