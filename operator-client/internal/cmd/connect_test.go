package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

type stubForward struct {
	pod    string
	remote int32
}

func (s *stubForward) Forward(ctx context.Context, namespace, pod string, local, remote int32) (int, func(), error) {
	s.pod = pod
	s.remote = remote
	return 54321, func() {}, nil
}

type stubExec struct {
	command []string
	env     []string
}

func (s *stubExec) Exec(ctx context.Context, namespace, pod, container string, command, env []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	s.command = command
	s.env = env
	return nil
}

type stubRunner struct {
	args []string
	env  []string
}

func (stubRunner) LookPath(string) (string, error) { return "psql", nil }

func (s *stubRunner) Run(ctx context.Context, name string, args []string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	s.args = args
	s.env = extraEnv
	return nil
}

func TestRunConnectForwardsPrimary(t *testing.T) {
	fwd := &stubForward{}
	run := &stubRunner{}
	var stderr bytes.Buffer
	cmd := newConnectCommand(&GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return connectClients(), nil
	}})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"my-db"})
	err := runConnect(cmd, &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return connectClients(), nil
	}}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca",
	}, connectDeps{forward: fwd, runner: run})
	if err != nil {
		t.Fatal(err)
	}
	if fwd.pod != "my-db-0" || fwd.remote != 5432 {
		t.Fatalf("forward pod=%s remote=%d", fwd.pod, fwd.remote)
	}
	if strings.Contains(strings.Join(run.args, " "), "s3cret-password") {
		t.Fatalf("password leaked into args: %v", run.args)
	}
	if !strings.Contains(strings.Join(run.env, "\n"), "PGPASSWORD=s3cret-password") {
		t.Fatal("missing PGPASSWORD")
	}
	if !strings.Contains(strings.Join(run.env, "\n"), "PGSSLMODE=verify-ca") {
		t.Fatal("missing sslmode")
	}
	if !strings.Contains(stderr.String(), "not DBClusterReady") || !strings.Contains(stderr.String(), "starting psql") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestConnectRejectsBothPortFlags(t *testing.T) {
	cmd := newConnectCommand(&GlobalOptions{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-db", "--local-port", "5432", "--port", "5433"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot both be set") {
		t.Fatalf("err %v", err)
	}
}

func TestConnectRejectsExecWithLocalPort(t *testing.T) {
	cmd := newConnectCommand(&GlobalOptions{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-db", "--exec", "--local-port", "5432"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--exec cannot be combined") {
		t.Fatalf("err %v", err)
	}
}

func TestConnectRejectsReadOnlyWithPod(t *testing.T) {
	cmd := newConnectCommand(&GlobalOptions{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-db", "--read-only", "--pod", "my-db-0"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected --read-only and --pod to conflict")
	}
}

func TestRunConnectReadOnlyForwardsReadPool(t *testing.T) {
	fwd := &stubForward{}
	err := runConnect(newConnectCommand(&GlobalOptions{}), &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return readPoolClients(), nil
	}}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca", readOnly: true,
	}, connectDeps{forward: fwd, runner: &stubRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if fwd.pod != "my-db-rp-0" {
		t.Fatalf("forward pod=%s", fwd.pod)
	}
}

func TestRunConnectReadOnlyUsesStandby(t *testing.T) {
	fwd := &stubForward{}
	err := runConnect(newConnectCommand(&GlobalOptions{}), &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return standbyClients(), nil
	}}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca", readOnly: true,
	}, connectDeps{forward: fwd, runner: &stubRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if fwd.pod != "my-db-standby" {
		t.Fatalf("forward pod=%s", fwd.pod)
	}
}

func TestRunConnectReadOnlyMissing(t *testing.T) {
	err := runConnect(newConnectCommand(&GlobalOptions{}), &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return connectClients(), nil
	}}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca", readOnly: true,
	}, connectDeps{forward: &stubForward{}, runner: &stubRunner{}})
	if err == nil || !strings.Contains(err.Error(), "no read-only pod") {
		t.Fatalf("err %v", err)
	}
}

func TestRunConnectExecSkipsForward(t *testing.T) {
	fwd := &stubForward{}
	ex := &stubExec{}
	var stderr bytes.Buffer
	cmd := newConnectCommand(&GlobalOptions{})
	cmd.SetErr(&stderr)
	err := runConnect(cmd, &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return connectClients(), nil
	}}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca", exec: true,
	}, connectDeps{forward: fwd, runner: &stubRunner{}, exec: ex})
	if err != nil {
		t.Fatal(err)
	}
	if fwd.pod != "" {
		t.Fatalf("forwarded to %s", fwd.pod)
	}
	if strings.Contains(strings.Join(ex.command, " "), "s3cret-password") {
		t.Fatalf("password leaked into command: %v", ex.command)
	}
	if !strings.Contains(strings.Join(ex.env, "\n"), "PGPASSWORD=s3cret-password") {
		t.Fatalf("env %v", ex.env)
	}
	joined := strings.Join(ex.command, " ")
	if !strings.Contains(joined, "psql") || !strings.Contains(joined, "postgres") {
		t.Fatalf("command %v", ex.command)
	}
	if !strings.Contains(stderr.String(), "exec: my-db-0/database") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestRunConnectRejectsSSLMode(t *testing.T) {
	err := runConnect(newConnectCommand(&GlobalOptions{}), &GlobalOptions{}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "prefer",
	}, connectDeps{})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err %v", err)
	}
}

func TestRunConnectMissingCA(t *testing.T) {
	c := connectClients()
	c.Typed = fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: "adbo-eugene"}, Data: map[string][]byte{"my-db": []byte("s3cret-password")}},
		databasePod(),
	)
	err := runConnect(newConnectCommand(&GlobalOptions{}), &GlobalOptions{NewClients: func() (*k8s.Clients, error) { return c, nil }}, connectOpts{
		cluster: "my-db", user: "postgres", dbname: "postgres", sslmode: "verify-ca",
	}, connectDeps{forward: &stubForward{}, runner: &stubRunner{}})
	if err == nil || !strings.Contains(err.Error(), "CA certificate") || strings.Contains(err.Error(), "s3cret-password") {
		t.Fatalf("err %v", err)
	}
}

func connectClients() *k8s.Clients {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		k8s.DBClusterGVR:  "DBClusterList",
		k8s.InstanceGVR:   "InstanceList",
		k8s.DBInstanceGVR: "DBInstanceList",
	}, dbClusterObj())
	return &k8s.Clients{
		Dynamic:   dyn,
		Typed:     fake.NewClientset(passwordSecret(), caSecret(), databasePod()),
		Namespace: "adbo-eugene",
		Context:   "test",
	}
}

func readPoolClients() *k8s.Clients {
	c := connectClients()
	rp := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBInstance",
		"metadata":   map[string]any{"name": "my-db-rp", "namespace": "adbo-eugene"},
		"spec": map[string]any{
			"instanceType": "ReadPool",
			"dbcParent":    map[string]any{"name": "my-db"},
		},
	}}
	c.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		k8s.DBClusterGVR:  "DBClusterList",
		k8s.InstanceGVR:   "InstanceList",
		k8s.DBInstanceGVR: "DBInstanceList",
	}, dbClusterObj(), rp)
	c.Typed = fake.NewClientset(passwordSecret(), caSecret(), databasePod(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "my-db-rp-0",
		Namespace: "adbo-eugene",
		Labels: map[string]string{
			cluster.LabelDBCluster:                       "my-db",
			cluster.LabelTaskType:                        cluster.TaskDatabase,
			"alloydbomni.internal.dbadmin.goog/instance": "my-db-rp",
		},
	}})
	return c
}

func standbyClients() *k8s.Clients {
	c := connectClients()
	c.Typed = fake.NewClientset(passwordSecret(), caSecret(), databasePod(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "my-db-standby",
		Namespace: "adbo-eugene",
		Labels: map[string]string{
			cluster.LabelDBCluster: "my-db",
			cluster.LabelTaskType:  cluster.TaskDatabase,
			cluster.LabelHARole:    cluster.RoleStandby,
		},
	}})
	return c
}

func dbClusterObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBCluster",
		"metadata":   map[string]any{"name": "my-db", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"primarySpec": map[string]any{}},
		"status": map[string]any{
			"phase": "Provisioning",
			"certificateReference": map[string]any{
				"certificateKey": "ca.crt",
				"secretRef":      map[string]any{"name": "my-ca"},
			},
		},
	}}
}

func passwordSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: "adbo-eugene"}, Data: map[string][]byte{"my-db": []byte("s3cret-password")}}
}

func caSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-ca", Namespace: "adbo-eugene"}, Data: map[string][]byte{"ca.crt": []byte("CERT")}}
}

func databasePod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "my-db-0",
		Namespace: "adbo-eugene",
		Labels: map[string]string{
			cluster.LabelDBCluster: "my-db",
			cluster.LabelTaskType:  cluster.TaskDatabase,
		},
	}}
}
