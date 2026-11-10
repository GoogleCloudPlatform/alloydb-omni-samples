package cmd

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

const (
	clusterReadyPhase = "DBClusterReady"

	// defaultDataDiskName is the disk the operator puts the database files on.
	// `adbo cluster scale --disk` resizes this same disk by name.
	defaultDataDiskName = "DataDisk"
	defaultDiskSize     = "10Gi"
	defaultCPU          = "1"

	// memoryBytesPerCPU is the --memory default: 8Gi per core, so --cpu 4 gets
	// 32Gi without a second flag.
	memoryBytesPerCPU = 8 * 1024 * 1024 * 1024

	generatedPasswordLength = 24

	// passwordAlphabet leaves out quotes, backslashes and shell metacharacters
	// so a generated password is safe to paste into psql or a connection string.
	passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789-_"
)

// createFlags are the local flags of `adbo cluster create`.
type createFlags struct {
	version      string
	cpu          string
	memory       string
	disk         string
	password     string
	passwordFile string
	showPassword bool
	standbys     int
	dryRun       bool
}

func newClusterCreateCommand(opts *GlobalOptions) *cobra.Command {
	f := createFlags{}
	var w *WaitFlags

	cmd := &cobra.Command{
		Use:   "create CLUSTER_NAME",
		Short: "Create a database cluster and its admin password secret",
		Long: "Create a DBCluster plus the db-pw-<name> Secret the operator requires.\n" +
			"A password is generated unless you pass --password or --password-file.\n" +
			"create waits for the cluster to reach " + clusterReadyPhase + "; pass --no-wait to return\n" +
			"as soon as the objects are accepted.",
		Example: "  adbo cluster create my-db --version 16.8.0\n" +
			"  adbo cluster create my-db --version 16.8.0 --show-password\n" +
			"  adbo cluster create my-db --version 16.8.0 --cpu 4 --disk 100Gi\n" +
			"  adbo cluster create my-db --version 16.8.0 --password-file ./pw --no-wait",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterCreate(cmd, opts, args[0], f, w.Enabled())
		},
	}

	cmd.Flags().StringVar(&f.version, "version", "", "database version, e.g. 16.8.0 (required)")
	cmd.Flags().StringVar(&f.cpu, "cpu", defaultCPU, "CPU cores for the primary, e.g. 2 or 500m")
	cmd.Flags().StringVar(&f.memory, "memory", "", "memory for the primary, e.g. 16Gi (default 8Gi per CPU)")
	cmd.Flags().StringVar(&f.disk, "disk", defaultDiskSize, "data disk size, e.g. 50Gi")
	cmd.Flags().StringVar(&f.password, "password", "", "admin password (visible in shell history; prefer --password-file)")
	cmd.Flags().StringVar(&f.passwordFile, "password-file", "", "file holding the admin password")
	cmd.Flags().BoolVar(&f.showPassword, "show-password", false,
		"print the generated password; by default only its location is printed")
	cmd.Flags().IntVar(&f.standbys, "standbys", 0,
		"standby instances for HA; each one needs the same cpu/memory/disk as the primary")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false,
		"send both objects to the API server for validation without creating them")
	// Shared --wait / --no-wait so every state-changing verb behaves the same.
	w = opts.AddWaitFlags(cmd)
	return cmd
}

func runClusterCreate(cmd *cobra.Command, opts *GlobalOptions, name string, f createFlags, wait bool) error {
	spec, err := planCreate(name, f)
	if err != nil {
		return err
	}
	password, generated, err := resolvePassword(f)
	if err != nil {
		return err
	}

	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	secretName := k8s.AdminSecretName(name)

	if f.dryRun {
		return dryRunCreate(ctx, cmd, c, name, secretName, spec)
	}

	// The DBCluster references the Secret by name, so the Secret goes first.
	reusedSecret, err := ensureAdminSecret(ctx, c, name, password, f.passwordGiven())
	if err != nil {
		return err
	}

	db := newDBClusterObject(name, c.Namespace, secretName, spec)
	if _, err := c.In(k8s.DBClusterGVR).Create(ctx, db, metav1.CreateOptions{}); err != nil {
		// Do not leave an orphan Secret behind for a cluster that never
		// existed. A Secret we reused was not ours to remove.
		if !reusedSecret {
			rollbackSecret(ctx, cmd, c, secretName)
		}
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("cluster %q already exists in namespace %q (context %q)", name, c.Namespace, c.Context)
		}
		return c.FormatErr(err)
	}

	result := created{
		name:       name,
		secretName: secretName,
		spec:       spec,
		password:   password,
		generated:  generated,
		reused:     reusedSecret,
	}
	if err := reportCreated(out, opts.Output, c, result, f.showPassword); err != nil {
		return err
	}

	if !wait {
		fmt.Fprintf(out, "Not waiting. Check progress with: adbo status %s\n", name)
		return nil
	}

	return waitForClusterPhase(ctx, cmd, opts, c, name, clusterReadyPhase)
}

// created is what runClusterCreate ended up doing, for reporting.
type created struct {
	name       string
	secretName string
	spec       clusterSpec
	password   string
	generated  bool // the password was generated rather than supplied
	reused     bool // the admin secret already existed
}

// reportCreated describes what was just created, as a table or as a summary
// object for scripts.
func reportCreated(out io.Writer, format string, c *k8s.Clients, r created, showPassword bool) error {
	if format == output.OutputJSON || format == output.OutputYAML {
		summary := map[string]any{
			"name":              r.name,
			"namespace":         c.Namespace,
			"secret":            r.secretName,
			"version":           r.spec.version,
			"cpu":               r.spec.cpu,
			"memory":            r.spec.memory,
			"disk":              r.spec.disk,
			"standbys":          r.spec.standbys,
			"reusedSecret":      r.reused,
			"passwordGenerated": r.generated,
		}
		// A password in -o json lands in logs and CI output, so it is opt-in.
		if r.generated && showPassword {
			summary["password"] = r.password
		}
		return output.Print(out, format, output.Table{}, summary)
	}

	fmt.Fprintf(out, "Created cluster %q in namespace %q (%s CPU, %s memory, %s disk, version %s).\n",
		r.name, c.Namespace, r.spec.cpu, r.spec.memory, r.spec.disk, r.spec.version)

	switch {
	case r.reused:
		// The password is whatever was already in the secret, possibly from a
		// cluster that no longer exists.
		fmt.Fprintf(out, "Reused the existing admin secret %q, which this command did not create; its password is unchanged.\n", r.secretName)
		printPasswordHint(out, c.Namespace, r.name, r.secretName)
	case r.generated && showPassword:
		fmt.Fprintf(out, "Generated admin password (stored in secret %q): %s\n", r.secretName, r.password)
	case r.generated:
		// Printing it by default would put it in shell history and CI logs.
		fmt.Fprintf(out, "Generated an admin password and stored it in secret %q.\n", r.secretName)
		printPasswordHint(out, c.Namespace, r.name, r.secretName)
	default:
		fmt.Fprintf(out, "Stored the password you supplied in secret %q.\n", r.secretName)
	}
	return nil
}

// printPasswordHint shows how to read the password back out of the secret.
// The bracket form of jsonpath is deliberate: the dotted form breaks on
// cluster names containing a hyphen.
func printPasswordHint(out io.Writer, namespace, cluster, secretName string) {
	fmt.Fprintf(out, "Read it with: kubectl get secret %s -n %s -o jsonpath=\"{.data['%s']}\" | base64 -d\n",
		secretName, namespace, cluster)
}

// passwordGiven reports whether the user supplied a password rather than
// letting create generate one.
func (f createFlags) passwordGiven() bool {
	return f.password != "" || f.passwordFile != ""
}

// clusterSpec is the validated, resolved form of the create flags.
type clusterSpec struct {
	version  string
	cpu      string
	memory   string
	disk     string
	standbys int
}

// cpuField is the value to write to spec.primarySpec.resources.cpu. The CRD
// accepts an int or a string; whole cores go in as ints to match what people
// write in YAML.
func (s clusterSpec) cpuField() any {
	if n, err := strconv.ParseInt(s.cpu, 10, 64); err == nil {
		return n
	}
	return s.cpu
}

// planCreate validates the flags and fills in the defaults that depend on
// other flags (memory is 8Gi per CPU unless --memory says otherwise).
func planCreate(name string, f createFlags) (clusterSpec, error) {
	var s clusterSpec
	if name == "" {
		return s, fmt.Errorf("CLUSTER_NAME must not be empty")
	}
	if f.version == "" {
		return s, fmt.Errorf("--version is required, e.g. --version 16.8.0")
	}
	s.version = f.version

	cpuText := f.cpu
	if cpuText == "" {
		cpuText = defaultCPU
	}
	cpu, err := resource.ParseQuantity(cpuText)
	if err != nil || cpu.Sign() <= 0 {
		return s, fmt.Errorf("--cpu %q is not a valid positive size", f.cpu)
	}
	s.cpu = cpu.String()

	if f.memory == "" {
		// Scale the default with the core count: 8Gi per core.
		bytes := big.NewInt(memoryBytesPerCPU)
		bytes.Mul(bytes, big.NewInt(cpu.MilliValue()))
		bytes.Div(bytes, big.NewInt(1000))
		s.memory = resource.NewQuantity(bytes.Int64(), resource.BinarySI).String()
	} else {
		mem, err := resource.ParseQuantity(f.memory)
		if err != nil || mem.Sign() <= 0 {
			return s, fmt.Errorf("--memory %q is not a valid positive size", f.memory)
		}
		s.memory = mem.String()
	}

	diskText := f.disk
	if diskText == "" {
		diskText = defaultDiskSize
	}
	disk, err := resource.ParseQuantity(diskText)
	if err != nil || disk.Sign() <= 0 {
		return s, fmt.Errorf("--disk %q is not a valid positive size", f.disk)
	}
	s.disk = disk.String()

	if f.standbys < 0 {
		return s, fmt.Errorf("--standbys must be 0 or more")
	}
	s.standbys = f.standbys

	if f.password != "" && f.passwordFile != "" {
		return s, fmt.Errorf("pass only one of --password or --password-file")
	}
	return s, nil
}

// newDBClusterObject builds the DBCluster the operator expects. Field layout
// matches samples/v1_dbcluster_ha.yaml.
func newDBClusterObject(name, namespace, secretName string, s clusterSpec) *unstructured.Unstructured {
	spec := map[string]any{
		"databaseVersion": s.version,
		"primarySpec": map[string]any{
			"adminUser": map[string]any{
				"passwordRef": map[string]any{"name": secretName},
			},
			"resources": map[string]any{
				"cpu":    s.cpuField(),
				"memory": s.memory,
				"disks": []any{
					map[string]any{"name": defaultDataDiskName, "size": s.disk},
				},
			},
		},
	}
	// Only set availability when HA is asked for: the operator already
	// defaults a single-instance cluster, and writing zeros reads as
	// deliberate configuration.
	if s.standbys > 0 {
		spec["availability"] = map[string]any{
			"numberOfStandbys":   int64(s.standbys),
			"enableAutoFailover": true,
			"enableAutoHeal":     true,
		}
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBCluster",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}}
}

// resolvePassword returns the password to store and whether create generated
// it. --password-file wins over --password; neither means generate one.
func resolvePassword(f createFlags) (string, bool, error) {
	if f.passwordFile != "" {
		b, err := os.ReadFile(f.passwordFile)
		if err != nil {
			return "", false, fmt.Errorf("reading --password-file: %w", err)
		}
		pw := strings.TrimRight(string(b), "\r\n")
		if pw == "" {
			return "", false, fmt.Errorf("--password-file %q is empty", f.passwordFile)
		}
		return pw, false, nil
	}
	if f.password != "" {
		return f.password, false, nil
	}
	pw, err := generatePassword(generatedPasswordLength)
	if err != nil {
		return "", false, err
	}
	return pw, true, nil
}

// generatePassword returns a crypto/rand password drawn from passwordAlphabet.
func generatePassword(n int) (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generating a password: %w", err)
		}
		b[i] = passwordAlphabet[k.Int64()]
	}
	return string(b), nil
}

// ensureAdminSecret creates db-pw-<name> with the cluster name as the data
// key, which is the layout the operator's passwordRef expects. An existing
// secret is left alone so a retried create cannot change a live password;
// that is only safe when the caller did not ask for a specific password.
func ensureAdminSecret(ctx context.Context, c *k8s.Clients, cluster, password string, passwordGiven bool) (reused bool, err error) {
	secretName := k8s.AdminSecretName(cluster)
	secrets := c.Typed.CoreV1().Secrets(c.Namespace)

	existing, getErr := secrets.Get(ctx, secretName, metav1.GetOptions{})
	switch {
	case getErr == nil:
		if passwordGiven {
			return false, fmt.Errorf("secret %q already exists in namespace %q; delete it or omit --password/--password-file to keep the current password",
				secretName, c.Namespace)
		}
		// The operator reads the password from a key named after the cluster.
		// An empty value is as broken as a missing one: the cluster would come
		// up with no usable admin password.
		value, ok := existing.Data[cluster]
		if !ok {
			if v, strOK := existing.StringData[cluster]; strOK {
				value, ok = []byte(v), true
			}
		}
		switch {
		case !ok:
			return false, fmt.Errorf("secret %q exists but has no %q key; the operator reads the password from that key",
				secretName, cluster)
		case len(value) == 0:
			return false, fmt.Errorf("secret %q has an empty %q key; delete it and retry so a password can be generated",
				secretName, cluster)
		}
		return true, nil
	case !apierrors.IsNotFound(getErr):
		return false, c.FormatErr(getErr)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: c.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		// StringData is base64-encoded by the API server.
		StringData: map[string]string{cluster: password},
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return false, c.FormatErr(err)
	}
	return false, nil
}

// dryRunCreate validates a create without writing anything.
//
// How much can be checked depends on the admin secret. Omni's create webhook
// resolves spec.primarySpec.adminUser.passwordRef and rejects a DBCluster
// whose secret does not exist -- including on a server-side dry run, which by
// definition has not created it. So when the secret is absent the flags and
// the spec are checked locally and that is said plainly, rather than sending
// a request that is guaranteed to be denied for the wrong reason.
func dryRunCreate(ctx context.Context, cmd *cobra.Command, c *k8s.Clients, name, secretName string, s clusterSpec) error {
	out := cmd.OutOrStdout()
	shape := fmt.Sprintf("%s CPU, %s memory, %s disk, version %s", s.cpu, s.memory, s.disk, s.version)
	if s.standbys > 0 {
		shape += fmt.Sprintf(", %d standby", s.standbys)
	}

	if _, err := c.Typed.CoreV1().Secrets(c.Namespace).Get(ctx, secretName, metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return c.FormatErr(err)
		}
		fmt.Fprintf(out, "Dry run: settings for cluster %q are valid (%s).\n", name, shape)
		fmt.Fprintf(out, "Checked locally only: the operator's webhook needs the secret %q to exist before it\n"+
			"will validate the cluster, and a dry run does not create it. A real run creates the secret first.\n", secretName)
		return nil
	}

	// The secret is already there, so the API server can run the full check:
	// schema validation plus Omni's admission webhooks.
	db := newDBClusterObject(name, c.Namespace, secretName, s)
	if _, err := c.In(k8s.DBClusterGVR).Create(ctx, db, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll},
	}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("cluster %q already exists in namespace %q (context %q)", name, c.Namespace, c.Context)
		}
		return c.FormatErr(err)
	}
	fmt.Fprintf(out, "Dry run: cluster %q accepted by the API server and not created (%s).\n", name, shape)
	fmt.Fprintf(out, "The existing secret %q would be reused.\n", secretName)
	return nil
}
