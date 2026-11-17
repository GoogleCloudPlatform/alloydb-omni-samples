package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/connect"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/forward"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// connectDeps is replaced in tests so they do not open a real tunnel or run psql.
type connectDeps struct {
	forward connect.Forwarder
	runner  connect.Runner
	exec    connect.Execer
}

func newConnectCommand(opts *GlobalOptions) *cobra.Command {
	var (
		user      string
		dbname    string
		localPort int32
		podName   string
		sslmode   string
		readOnly  bool
		execInPod bool
	)
	cmd := &cobra.Command{
		Use:   "connect [CLUSTER_NAME]",
		Short: "Open a psql session to a DBCluster",
		Long: `Fetch the admin password and CA, port-forward a database pod to 127.0.0.1,
and start a local psql. The default target is the primary.

--read-only targets a ReadPool pod, or a standby when the cluster has no read pool.
--exec runs psql inside the database container and does not port-forward.

The password is passed only through PGPASSWORD and is not printed.
The global --timeout does not apply; leave the session with \q or Ctrl+C.
A local psql is required unless --exec is set.`,
		Example: "  adbo connect my-db\n" +
			"  adbo connect my-db --local-port 5432\n" +
			"  adbo connect my-db --read-only\n" +
			"  adbo connect my-db --exec\n" +
			"  adbo connect my-db --pod my-db-0 --sslmode disable",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("local-port") && cmd.Flags().Changed("port") {
				return fmt.Errorf("--local-port and --port cannot both be set")
			}
			if execInPod && (cmd.Flags().Changed("local-port") || cmd.Flags().Changed("port")) {
				return fmt.Errorf("--exec cannot be combined with --local-port")
			}
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runConnect(cmd, opts, connectOpts{
				cluster:   name,
				user:      user,
				dbname:    dbname,
				localPort: localPort,
				pod:       podName,
				sslmode:   sslmode,
				readOnly:  readOnly,
				exec:      execInPod,
			}, connectDeps{})
		},
	}
	cmd.Flags().StringVar(&user, "username", "postgres", "database user")
	cmd.Flags().StringVar(&dbname, "dbname", "postgres", "database name")
	cmd.Flags().Int32Var(&localPort, "local-port", 0, "local port (0 picks a free port)")
	cmd.Flags().Int32Var(&localPort, "port", 0, "alias of --local-port")
	cmd.Flags().StringVar(&podName, "pod", "", "database pod to use (default: the primary)")
	cmd.Flags().StringVar(&sslmode, "sslmode", "verify-ca", "psql sslmode: verify-ca, require, or disable")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "connect to a ReadPool pod, or a standby if there is no read pool")
	cmd.Flags().BoolVar(&execInPod, "exec", false, "run psql inside the database container instead of port-forwarding")
	cmd.MarkFlagsMutuallyExclusive("read-only", "pod")
	return cmd
}

type connectOpts struct {
	cluster   string
	user      string
	dbname    string
	localPort int32
	pod       string
	sslmode   string
	readOnly  bool
	exec      bool
}

func runConnect(cmd *cobra.Command, opts *GlobalOptions, in connectOpts, deps connectDeps) error {
	switch in.sslmode {
	case "verify-ca", "require", "disable":
	default:
		return fmt.Errorf("--sslmode must be verify-ca, require, or disable")
	}
	if in.localPort < 0 || in.localPort > 65535 {
		return fmt.Errorf("--local-port must be between 0 and 65535")
	}
	if in.exec && in.localPort != 0 {
		return fmt.Errorf("--exec cannot be combined with --local-port")
	}
	if in.user == "" || in.dbname == "" {
		return fmt.Errorf("--username and --dbname are required")
	}
	if errs := validation.NameIsDNSSubdomain(in.cluster, false); len(errs) > 0 {
		return fmt.Errorf("invalid cluster name %q: %s", in.cluster, errs[0])
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	if c.Typed == nil {
		return fmt.Errorf("kubernetes client is not configured")
	}

	obj, err := c.In(k8s.DBClusterGVR).Get(ctx, in.cluster, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	if phase := cluster.Phase(obj); phase != "" && phase != cluster.PhaseReady {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: DBCluster %q phase is %q, not %s\n", in.cluster, phase, cluster.PhaseReady)
	}

	ns := obj.GetNamespace()
	if ns == "" {
		ns = c.Namespace
	}
	pod, err := targetPod(ctx, c, ns, in.cluster, in.pod, in.readOnly)
	if err != nil {
		return err
	}
	password, err := adminPassword(ctx, c, ns, in.cluster, obj)
	if err != nil {
		return err
	}

	if in.exec {
		ex := deps.exec
		if ex == nil {
			ex = connect.KubeExec{REST: c.REST, Typed: c.Typed}
		}
		return connect.Session{
			Namespace: ns,
			Pod:       pod,
			Container: cluster.ContainerDatabase,
			User:      in.user,
			Database:  in.dbname,
			Password:  string(password),
		}.Exec(ctx, ex, cmd.ErrOrStderr(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	}

	var caPath string
	var cleanup func()
	if in.sslmode != "disable" {
		caPath, cleanup, err = fetchCA(ctx, c, ns, in.cluster, obj, in.sslmode)
		if err != nil {
			return err
		}
		if cleanup != nil {
			defer cleanup()
		}
	}

	fwd := deps.forward
	if fwd == nil {
		fwd = forward.Tunnel{REST: c.REST}
	}
	runner := deps.runner
	if runner == nil {
		runner = connect.ExecRunner{}
	}
	return connect.Session{
		Namespace:  ns,
		Pod:        pod,
		LocalPort:  in.localPort,
		RemotePort: cluster.DBPort(obj),
		User:       in.user,
		Database:   in.dbname,
		SSLMode:    in.sslmode,
		Password:   string(password),
		CAPath:     caPath,
	}.Start(ctx, fwd, runner, cmd.ErrOrStderr(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func targetPod(ctx context.Context, c *k8s.Clients, namespace, name, explicit string, readOnly bool) (string, error) {
	if readOnly {
		return readOnlyPod(ctx, c, namespace, name)
	}
	return primaryPod(ctx, c, namespace, name, explicit)
}

func primaryPod(ctx context.Context, c *k8s.Clients, namespace, name, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	pods, err := listDatabasePods(ctx, c, namespace, name)
	if err != nil {
		return "", err
	}
	instances, _ := instanceRoles(ctx, c, namespace, name)
	return cluster.SelectPrimary(pods, instances, "")
}

func readOnlyPod(ctx context.Context, c *k8s.Clients, namespace, name string) (string, error) {
	pods, err := listDatabasePods(ctx, c, namespace, name)
	if err != nil {
		return "", err
	}
	pools, err := readPoolNames(ctx, c, namespace, name)
	if err != nil {
		return "", err
	}
	return cluster.SelectReadOnly(pods, pools)
}

func listDatabasePods(ctx context.Context, c *k8s.Clients, namespace, name string) ([]cluster.PodInfo, error) {
	if namespace == "" {
		namespace = c.Namespace
	}
	list, err := c.Typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: cluster.PodSelector(name),
	})
	if err != nil {
		return nil, c.FormatErr(err)
	}
	pods := make([]cluster.PodInfo, len(list.Items))
	for i, pod := range list.Items {
		pods[i] = cluster.PodInfo{Name: pod.Name, Labels: pod.Labels}
	}
	return pods, nil
}

func readPoolNames(ctx context.Context, c *k8s.Clients, namespace, clusterName string) ([]string, error) {
	list, err := c.InNamespace(k8s.DBInstanceGVR, namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, c.FormatErr(err)
	}
	var names []string
	for i := range list.Items {
		obj := &list.Items[i]
		typ, _, _ := unstructured.NestedString(obj.Object, "spec", "instanceType")
		parent, _, _ := unstructured.NestedString(obj.Object, "spec", "dbcParent", "name")
		if typ == "ReadPool" && parent == clusterName {
			names = append(names, obj.GetName())
		}
	}
	return names, nil
}

func instanceRoles(ctx context.Context, c *k8s.Clients, namespace, name string) ([]cluster.InstanceInfo, error) {
	list, err := c.InNamespace(k8s.InstanceGVR, namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// HA role is optional. A single database pod does not need it.
		return nil, nil
	}
	var out []cluster.InstanceInfo
	for i := range list.Items {
		obj := &list.Items[i]
		if !cluster.Belongs(obj, name) {
			continue
		}
		out = append(out, cluster.InstanceInfo{
			Name: obj.GetName(),
			Role: obj.GetLabels()[cluster.LabelHARole],
		})
	}
	return out, nil
}

func adminPassword(ctx context.Context, c *k8s.Clients, namespace, name string, obj *unstructured.Unstructured) ([]byte, error) {
	secretName := cluster.PasswordSecretName(name, obj)
	sec, err := c.Typed.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, c.FormatErr(err)
	}
	return cluster.SecretBytes(sec, name)
}

func fetchCA(ctx context.Context, c *k8s.Clients, namespace, name string, obj *unstructured.Unstructured, sslmode string) (string, func(), error) {
	secretName, key := cluster.CASecret(name, obj)
	sec, err := c.Typed.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if sslmode == "verify-ca" {
			return "", nil, fmt.Errorf("the cluster has not published a CA certificate yet (%s): %w", secretName, c.FormatErr(err))
		}
		return "", nil, nil
	}
	pem, err := cluster.SecretBytes(sec, key)
	if err != nil {
		if sslmode == "verify-ca" {
			return "", nil, fmt.Errorf("the cluster has not published a CA certificate yet: %w", err)
		}
		return "", nil, nil
	}
	return connect.WriteCAFile(pem)
}
