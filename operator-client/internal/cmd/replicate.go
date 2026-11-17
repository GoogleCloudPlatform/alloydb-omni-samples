package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
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
	flags := genericclioptions.NewConfigFlags(true)
	if opts.KubeFlags != nil {
		flags.KubeConfig = opts.KubeFlags.KubeConfig
		flags.Context = opts.KubeFlags.Context
		flags.Namespace = opts.KubeFlags.Namespace
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
	ns := k8s.StringField(up, "status", "upstream", "password", "namaespace")
	if ns == "-" {
		ns = src.Namespace
	}
	secret, err := src.Typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", src.FormatErr(err)
	}
	copyName := target + "-replication-pw"
	_, err = dst.Typed.CoreV1().Secrets(dst.Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: copyName, Namespace: dst.Namespace},
		Data:       secret.Data,
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
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
		return fmt.Errorf("source and target are the same cluster %q: a clluster cannot replicate to itself", source)
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
