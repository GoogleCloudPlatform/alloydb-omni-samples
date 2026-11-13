package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// errBoom stands in for an API-server failure in reactors.
var errBoom = errors.New("boom")

// storedSecretPassword returns the password create wrote for cluster.
// The typed fake does not fold StringData into Data, so check both.
func storedSecretPassword(t *testing.T, typed *typedfake.Clientset, cluster string) string {
	t.Helper()
	s, err := typed.CoreV1().Secrets(testNamespace).Get(context.TODO(), k8s.AdminSecretName(cluster), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("admin secret for %q not created: %v", cluster, err)
	}
	if v, ok := s.StringData[cluster]; ok {
		return v
	}
	return string(s.Data[cluster])
}

func TestClusterCreateMakesSecretAndCluster(t *testing.T) {
	opts, dyn, typed := crudFakes(t, nil)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}

	db, err := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), "my-db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("DBCluster not created: %v", err)
	}
	if got, _, _ := unstructured.NestedString(db.Object, "spec", "databaseVersion"); got != "16.8.0" {
		t.Fatalf("databaseVersion = %q, want 16.8.0", got)
	}
	// Whole cores go in as an int, matching what users write in YAML.
	if got, found, _ := unstructured.NestedInt64(db.Object, "spec", "primarySpec", "resources", "cpu"); !found || got != 1 {
		t.Fatalf("cpu = %v (found %v), want int64 1", got, found)
	}
	if got, _, _ := unstructured.NestedString(db.Object, "spec", "primarySpec", "resources", "memory"); got != "8Gi" {
		t.Fatalf("memory = %q, want the 8Gi-per-CPU default", got)
	}
	disks, _, _ := unstructured.NestedSlice(db.Object, "spec", "primarySpec", "resources", "disks")
	if len(disks) != 1 {
		t.Fatalf("want exactly one disk, got %v", disks)
	}
	d := disks[0].(map[string]any)
	if d["name"] != defaultDataDiskName || d["size"] != defaultDiskSize {
		t.Fatalf("disk = %v, want %s/%s", d, defaultDataDiskName, defaultDiskSize)
	}
	// The operator reads the password through this reference.
	if got, _, _ := unstructured.NestedString(db.Object, "spec", "primarySpec", "adminUser", "passwordRef", "name"); got != "db-pw-my-db" {
		t.Fatalf("passwordRef.name = %q, want db-pw-my-db", got)
	}

	// Secret key must be the cluster name, not "password".
	s, err := typed.CoreV1().Secrets(testNamespace).Get(context.TODO(), "db-pw-my-db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	if s.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret type = %q, want Opaque", s.Type)
	}
	if _, ok := s.StringData["my-db"]; !ok {
		if _, ok := s.Data["my-db"]; !ok {
			t.Fatalf("secret has no %q key: %+v", "my-db", s)
		}
	}
	// The password itself must not be printed by default.
	if !strings.Contains(out, `stored it in secret "db-pw-my-db"`) {
		t.Fatalf("create should say where the password went:\n%s", out)
	}
	if got := storedSecretPassword(t, typed, "my-db"); strings.Contains(out, got) {
		t.Fatalf("the generated password must not be printed without --show-password:\n%s", out)
	}
	if !strings.Contains(out, "kubectl get secret db-pw-my-db") {
		t.Fatalf("create should say how to read the password:\n%s", out)
	}
}

func TestClusterCreateGeneratesPasswordUnlessGiven(t *testing.T) {
	// Given a password, store exactly that.
	opts, _, typed := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--password", "sekret123", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	if got := storedSecretPassword(t, typed, "my-db"); got != "sekret123" {
		t.Fatalf("stored password = %q, want the one passed to --password", got)
	}

	// Not given, generate one: right length, from the safe alphabet, and
	// different on a second run.
	var seen []string
	for _, name := range []string{"gen-a", "gen-b"} {
		o, _, tp := crudFakes(t, nil)
		if _, err := runCRUDCmd(t, newClusterCreateCommand(o),
			name, "--version", "16.8.0", "--no-wait"); err != nil {
			t.Fatal(err)
		}
		pw := storedSecretPassword(t, tp, name)
		if len(pw) != generatedPasswordLength {
			t.Fatalf("generated password %q has length %d, want %d", pw, len(pw), generatedPasswordLength)
		}
		if strings.ContainsAny(pw, `'"\$ `) {
			t.Fatalf("generated password %q contains a shell/psql-hostile character", pw)
		}
		seen = append(seen, pw)
	}
	if seen[0] == seen[1] {
		t.Fatalf("two creates produced the same password %q", seen[0])
	}
}

func TestClusterCreateShowPasswordPrintsIt(t *testing.T) {
	opts, _, typed := crudFakes(t, nil)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--show-password", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	pw := storedSecretPassword(t, typed, "my-db")
	if !strings.Contains(out, pw) {
		t.Fatalf("--show-password should print the password:\n%s", out)
	}
}

func TestClusterCreateJSONOmitsPasswordByDefault(t *testing.T) {
	opts, _, typed := crudFakes(t, nil)
	opts.Output = output.OutputJSON
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	pw := storedSecretPassword(t, typed, "my-db")
	if strings.Contains(out, pw) {
		t.Fatalf("-o json must not carry the password without --show-password:\n%s", out)
	}
	if !strings.Contains(out, `"passwordGenerated": true`) {
		t.Fatalf("-o json should still say a password was generated:\n%s", out)
	}
}

func TestClusterCreatePasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	// A trailing newline from `echo` must not become part of the password.
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, _, typed := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--password-file", path, "--no-wait"); err != nil {
		t.Fatal(err)
	}
	if got := storedSecretPassword(t, typed, "my-db"); got != "from-file" {
		t.Fatalf("stored password = %q, want %q", got, "from-file")
	}
}

func TestClusterCreateMemoryDefaultScalesWithCPU(t *testing.T) {
	for _, tc := range []struct{ cpu, wantMemory string }{
		{"1", "8Gi"},
		{"4", "32Gi"},
		{"500m", "4Gi"},
	} {
		opts, dyn, _ := crudFakes(t, nil)
		if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
			"my-db", "--version", "16.8.0", "--cpu", tc.cpu, "--no-wait"); err != nil {
			t.Fatalf("cpu %s: %v", tc.cpu, err)
		}
		db, err := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
			Get(context.TODO(), "my-db", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, _, _ := unstructured.NestedString(db.Object, "spec", "primarySpec", "resources", "memory")
		if got != tc.wantMemory {
			t.Fatalf("--cpu %s gave memory %q, want %q", tc.cpu, got, tc.wantMemory)
		}
	}

	// An explicit --memory wins over the per-CPU default.
	opts, dyn, _ := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--cpu", "4", "--memory", "12Gi", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	db, _ := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), "my-db", metav1.GetOptions{})
	if got, _, _ := unstructured.NestedString(db.Object, "spec", "primarySpec", "resources", "memory"); got != "12Gi" {
		t.Fatalf("memory = %q, want the explicit 12Gi", got)
	}
}

func TestClusterCreateRejectsBadFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing version", []string{"my-db", "--no-wait"}},
		{"bad cpu", []string{"my-db", "--version", "16.8.0", "--cpu", "zero"}},
		{"negative cpu", []string{"my-db", "--version", "16.8.0", "--cpu", "-1"}},
		{"bad memory", []string{"my-db", "--version", "16.8.0", "--memory", "lots"}},
		{"bad disk", []string{"my-db", "--version", "16.8.0", "--disk", "big"}},
		{"both password flags", []string{"my-db", "--version", "16.8.0", "--password", "x", "--password-file", "y"}},
	} {
		opts, _, _ := crudFakes(t, nil)
		if _, err := runCRUDCmd(t, newClusterCreateCommand(opts), tc.args...); err == nil {
			t.Fatalf("%s should fail", tc.name)
		}
	}
}

func TestClusterCreateReusesExistingSecret(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"my-db": []byte("old-password")},
	}

	// No password asked for: keep the live password rather than rotating it.
	opts, _, typed := crudFakes(t, nil, existing)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Reused the existing admin secret") {
		t.Fatalf("create should say it reused the secret:\n%s", out)
	}
	if got := storedSecretPassword(t, typed, "my-db"); got != "old-password" {
		t.Fatalf("existing password was overwritten with %q", got)
	}

	// A password was asked for, so silently keeping the old one would be wrong.
	opts2, _, _ := crudFakes(t, nil, existing.DeepCopy())
	_, err = runCRUDCmd(t, newClusterCreateCommand(opts2),
		"my-db", "--version", "16.8.0", "--password", "new-one", "--no-wait")
	if err == nil {
		t.Fatal("create with --password should fail when the secret already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error should explain the conflict, got: %v", err)
	}
}

func TestClusterCreateRejectsSecretWithWrongKey(t *testing.T) {
	// The operator reads the password from a key named after the cluster.
	wrong := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: testNamespace},
		Data:       map[string][]byte{"password": []byte("x")},
	}
	opts, _, _ := crudFakes(t, nil, wrong)
	_, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err == nil {
		t.Fatal("a secret without the cluster-name key should be rejected")
	}
	if !strings.Contains(err.Error(), "my-db") {
		t.Fatalf("error should name the expected key, got: %v", err)
	}
}

func TestClusterCreateRollsBackSecretWhenClusterFails(t *testing.T) {
	opts, dyn, typed := crudFakes(t, nil)
	dyn.PrependReactor("create", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(errBoom)
	})

	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--no-wait"); err == nil {
		t.Fatal("create should fail when the DBCluster cannot be created")
	}
	// An orphan secret for a cluster that never existed would block a retry
	// that passes --password.
	if _, err := typed.CoreV1().Secrets(testNamespace).
		Get(context.TODO(), "db-pw-my-db", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret should have been rolled back, Get returned %v", err)
	}
}

// A Ctrl+C mid-create cancels the command's context, but the secret still has
// to go: an orphan blocks the retry. The fake clients ignore contexts, so this
// checks the context rollback actually runs on rather than the delete itself.
func TestCleanupContextOutlivesACancelledCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanup, done := cleanupContext(ctx)
	defer done()
	if err := cleanup.Err(); err != nil {
		t.Fatalf("cleanup must outlive the cancelled command, got %v", err)
	}
	if _, ok := cleanup.Deadline(); !ok {
		t.Fatal("cleanup needs a deadline so a wedged API server cannot hang the command")
	}
}

func TestClusterCreateAlreadyExists(t *testing.T) {
	existing := testDBCluster("my-db", clusterReadyPhase)
	opts, _, _ := crudFakes(t, []runtime.Object{existing})
	_, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err == nil {
		t.Fatal("creating an existing cluster should fail")
	}
	for _, want := range []string{"already exists", "my-db", testNamespace} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

// waitTestCmd is a bare command with a context, for driving the waiter
// directly. The create command itself is covered with --no-wait; these cover
// the internal/wait wiring without racing the fake's watch stream.
func waitTestCmd(buf *bytes.Buffer) *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(buf)
	c.SetErr(buf)
	c.SetContext(context.Background())
	return c
}

func TestClusterCreateWaitSucceedsWhenReady(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{testDBCluster("my-db", clusterReadyPhase)})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase); err != nil {
		t.Fatal(err)
	}
	// A cluster already ready when the wait starts must not be missed: the
	// waiter checks after its initial List, not only on later watch events.
	if !strings.Contains(buf.String(), "is "+clusterReadyPhase) {
		t.Fatalf("waiter should confirm readiness:\n%s", buf.String())
	}
}

func TestClusterCreateWaitTimesOut(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{testDBCluster("my-db", "DBClusterBootstrapping")})
	opts.Timeout = 50 * time.Millisecond
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	if err == nil {
		t.Fatal("a cluster that never becomes ready should time out")
	}
	for _, want := range []string{"did not reach", clusterReadyPhase, "adbo status my-db"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error missing %q: %v", want, err)
		}
	}
}

// shortIncidentPolling makes the incident watcher spin fast.
func shortIncidentPolling(t *testing.T) {
	t.Helper()
	prev := incidentPollInterval
	incidentPollInterval = time.Millisecond
	t.Cleanup(func() { incidentPollInterval = prev })
}

func TestClusterCreateWaitFailsFastOnCriticalIncident(t *testing.T) {
	shortIncidentPolling(t)
	failing := testDBCluster("my-db", "DBClusterUnknown")
	if err := unstructured.SetNestedSlice(failing.Object, []any{
		map[string]any{"code": "DBSE0013", "message": "Database instance is unhealthy"},
	}, "status", "criticalIncidents"); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := crudFakes(t, []runtime.Object{failing})
	// A generous deadline: the point is that the incident ends the wait long
	// before --timeout would, which is what used to hang for 30 minutes.
	opts.Timeout = 30 * time.Second
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	start := time.Now()
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cluster with a critical incident should not be reported ready")
	}
	if !strings.Contains(err.Error(), "[DBSE0013] Database instance is unhealthy") {
		t.Fatalf("error should carry the incident, got: %v", err)
	}
	if !strings.Contains(err.Error(), "critical incidents") {
		t.Fatalf("error should read as a failure, not a timeout: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("expected to fail fast, took %s of a 30s deadline", elapsed)
	}
}

func TestClusterWaitDoesNotFailOnTheOperatorsSchedulingIncident(t *testing.T) {
	// A live operator posts DBSE0085 for a pod the scheduler has already
	// placed and withdraws it about forty seconds later, which was long
	// enough to abort creates that then came up fine. Only the scheduler's
	// own verdict ends the wait now.
	shortIncidentPolling(t)
	noisy := testDBCluster("my-db", "DBClusterReconciling")
	if err := unstructured.SetNestedSlice(noisy.Object, []any{
		map[string]any{"code": "DBSE0085", "message": "Platform: Pod is unschedulable."},
	}, "status", "criticalIncidents"); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := crudFakes(t, []runtime.Object{noisy})
	// Long enough for the informer to sync, so the deadline is what ends it.
	opts.Timeout = 2 * time.Second
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	if err == nil {
		t.Fatal("this cluster never becomes ready, so the wait should end on its deadline")
	}
	if strings.Contains(err.Error(), "reported critical incidents while waiting") {
		t.Fatalf("a scheduling incident must not end the wait on its own: %v", err)
	}
	// It still has to reach the user once the wait really does run out.
	if !strings.Contains(err.Error(), "DBSE0085") {
		t.Fatalf("the timeout should still report what the operator said: %v", err)
	}
}

func TestClusterCreateWaitReportsCriticalIncidents(t *testing.T) {
	failing := testDBCluster("my-db", "DBClusterUnknown")
	if err := unstructured.SetNestedSlice(failing.Object, []any{
		map[string]any{"code": "DBSE0013", "message": "Database instance is unhealthy"},
	}, "status", "criticalIncidents"); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := crudFakes(t, []runtime.Object{failing})
	// Long enough for the waiter's informer to sync and observe the object:
	// the incident can only be reported if the initial List was actually seen.
	opts.Timeout = 2 * time.Second
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	if err == nil {
		t.Fatal("a cluster with a critical incident never becomes ready")
	}
	// DBCluster has no "Failed" phase, so the incident is the only actionable
	// detail; it must reach the user instead of a bare deadline message.
	if !strings.Contains(err.Error(), "[DBSE0013] Database instance is unhealthy") {
		t.Fatalf("error should carry the incident, got: %v", err)
	}
}

func TestClusterCreateHASpecSatisfiesTheCRD(t *testing.T) {
	// spec.availability requires enableStandbyAsReadReplica and the CRD gives
	// it no default, so omitting it risks the API server rejecting every
	// --standbys create with "Required value".
	opts, dyn, _ := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--standbys", "1", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	db, err := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), "my-db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	av, found, err := unstructured.NestedMap(db.Object, "spec", "availability")
	if err != nil || !found {
		t.Fatalf("--standbys should write spec.availability (found %v): %v", found, err)
	}
	if _, ok := av["enableStandbyAsReadReplica"]; !ok {
		t.Fatalf("spec.availability must carry enableStandbyAsReadReplica: %v", av)
	}
	if av["numberOfStandbys"] != int64(1) {
		t.Fatalf("numberOfStandbys = %v, want int64 1", av["numberOfStandbys"])
	}
}

func TestClusterCreateRejectsTooManyStandbysBeforeWritingAnything(t *testing.T) {
	// The CRD allows at most five. Catching it here matters because the admin
	// secret is written first: a server-side rejection means a create that
	// half happened and then had to roll back.
	opts, dyn, typed := crudFakes(t, nil)
	_, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--standbys", "6", "--no-wait")
	if err == nil {
		t.Fatal("--standbys 6 should be rejected")
	}
	if !strings.Contains(err.Error(), "between 0 and 5") {
		t.Fatalf("error should name the limit: %v", err)
	}
	if len(typed.Actions()) != 0 || len(dyn.Actions()) != 0 {
		t.Fatal("a rejected flag must not reach the API server at all")
	}
}

func TestClusterCreateJSONLeavesStdoutParseable(t *testing.T) {
	// The summary and the lines around it used to share stdout, so -o json
	// emitted a document followed by prose: not JSON at all.
	opts, _, _ := crudFakes(t, nil)
	opts.Output = output.OutputJSON

	stdout, stderr, err := runCRUDCmdSplit(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("-o json should leave stdout parseable: %v\nstdout: %s", err, stdout)
	}
	if got["name"] != "my-db" {
		t.Fatalf("summary = %v", got)
	}
	if !strings.Contains(stderr, "Not waiting") {
		t.Fatalf("the --no-wait note should still reach stderr:\n%s", stderr)
	}
}

func TestClusterCreateJSONSummary(t *testing.T) {
	opts, _, _ := crudFakes(t, nil)
	opts.Output = output.OutputJSON
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	// Strip the trailing "Not waiting" line before decoding.
	brace := strings.LastIndex(out, "}")
	var got map[string]any
	if err := json.Unmarshal([]byte(out[:brace+1]), &got); err != nil {
		t.Fatalf("-o json should emit a decodable summary: %v\n%s", err, out)
	}
	if got["name"] != "my-db" || got["secret"] != "db-pw-my-db" || got["memory"] != "8Gi" {
		t.Fatalf("summary = %v", got)
	}
	if got["passwordGenerated"] != true {
		t.Fatalf("summary should record that a password was generated: %v", got)
	}
}

func TestClusterCreateWaitFailsFastOnUnschedulablePod(t *testing.T) {
	shortIncidentPolling(t)
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "al-905c-my-db-0", Namespace: testNamespace,
			Labels: map[string]string{dbClusterPodLabel: "my-db"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/1 nodes are available: 1 Insufficient memory.",
			}},
		},
	}
	opts, _, _ := crudFakes(t, []runtime.Object{testDBCluster("my-db", "DBClusterBootstrapping")}, pending)
	// Generous deadline: the point is that we give up long before it expires.
	opts.Timeout = 30 * time.Second
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	start := time.Now()
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cluster whose pod cannot be scheduled should not be reported ready")
	}
	if !strings.Contains(err.Error(), "cannot be scheduled") ||
		!strings.Contains(err.Error(), "Insufficient memory") {
		t.Fatalf("error should carry the scheduler's reason, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--cpu/--memory/--disk") {
		t.Fatalf("error should say how to fix it, got: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("expected to fail fast, took %s of a 30s deadline", elapsed)
	}
}

func TestClusterCreateWaitToleratesBrieflyPendingPod(t *testing.T) {
	// Every normal create has a Pending moment; one sighting must not fail.
	shortIncidentPolling(t)
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "al-905c-my-db-0", Namespace: testNamespace,
			Labels: map[string]string{dbClusterPodLabel: "my-db"}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "binding"}},
		},
	}
	opts, _, typed := crudFakes(t, []runtime.Object{testDBCluster("my-db", "DBClusterBootstrapping")}, pending)
	opts.Timeout = 200 * time.Millisecond
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	// Scheduled before the strike count is reached.
	go func() {
		pending.Status.Phase = corev1.PodRunning
		_, _ = typed.CoreV1().Pods(testNamespace).UpdateStatus(context.TODO(), pending, metav1.UpdateOptions{})
	}()

	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	if err == nil {
		t.Fatal("expected the deadline to be what ends this wait")
	}
	if strings.Contains(err.Error(), "cannot be scheduled") {
		t.Fatalf("a transiently pending pod must not be reported unschedulable: %v", err)
	}
}

func TestSchedulingFailureDoesNotConfusePrefixedClusterNames(t *testing.T) {
	// "my-db-2"'s pod name contains "-my-db-" too, so name matching would fail
	// a healthy "my-db" wait because of a different cluster's pod. The label
	// keeps them apart.
	other := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "al-905c-my-db-2-0", Namespace: testNamespace,
			Labels: map[string]string{dbClusterPodLabel: "my-db-2"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Message: "0/1 nodes are available: 1 Insufficient memory.",
			}},
		},
	}
	opts, _, _ := crudFakes(t, nil, other)
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	if msg := schedulingFailure(context.Background(), c, "my-db"); msg != "" {
		t.Fatalf("my-db must not pick up my-db-2's pod, got: %q", msg)
	}
	if msg := schedulingFailure(context.Background(), c, "my-db-2"); msg == "" {
		t.Fatal("my-db-2 should see its own unschedulable pod")
	}
}

func TestClusterCreateStandbysSetsAvailability(t *testing.T) {
	opts, dyn, _ := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--standbys", "2", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	db, err := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), "my-db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n, found, _ := unstructured.NestedInt64(db.Object, "spec", "availability", "numberOfStandbys")
	if !found || n != 2 {
		t.Fatalf("numberOfStandbys = %v (found %v), want 2", n, found)
	}
	// HA is meaningless without failover, so both come on together.
	for _, field := range []string{"enableAutoFailover", "enableAutoHeal"} {
		v, found, _ := unstructured.NestedBool(db.Object, "spec", "availability", field)
		if !found || !v {
			t.Fatalf("%s = %v (found %v), want true", field, v, found)
		}
	}
}

func TestClusterCreateOmitsAvailabilityWithoutStandbys(t *testing.T) {
	// Writing zeros would read as deliberate HA configuration.
	opts, dyn, _ := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	db, _ := dyn.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), "my-db", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedMap(db.Object, "spec", "availability"); found {
		t.Fatal("spec.availability should be absent when --standbys is 0")
	}
}

func TestClusterCreateRejectsNegativeStandbys(t *testing.T) {
	opts, _, _ := crudFakes(t, nil)
	if _, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--standbys", "-1", "--no-wait"); err == nil {
		t.Fatal("--standbys -1 should fail")
	}
}

func TestClusterCreateRejectsSecretWithEmptyValue(t *testing.T) {
	// A present-but-empty key leaves the cluster with no usable password,
	// which is as broken as a missing key.
	empty := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: testNamespace},
		Data:       map[string][]byte{"my-db": {}},
	}
	opts, _, _ := crudFakes(t, nil, empty)
	_, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err == nil {
		t.Fatal("an empty password value should be rejected")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should say the value is empty, got: %v", err)
	}
}

func TestClusterCreateReusedSecretSaysHowToReadIt(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: testNamespace},
		Data:       map[string][]byte{"my-db": []byte("old-password")},
	}
	opts, _, _ := crudFakes(t, nil, existing)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	// The password may be from a cluster that no longer exists, so the user
	// has to be told it is not one this run generated, and how to read it.
	if !strings.Contains(out, "did not create") {
		t.Fatalf("output should flag that the secret predates this create:\n%s", out)
	}
	if !strings.Contains(out, "kubectl get secret db-pw-my-db") {
		t.Fatalf("output should say how to read the reused password:\n%s", out)
	}
}

func TestClusterCreateDryRunWithoutSecretChecksLocallyOnly(t *testing.T) {
	// Omni's webhook resolves passwordRef, so with no secret present there is
	// nothing the server can usefully validate -- say so instead of sending a
	// request that is denied for a misleading reason.
	opts, dyn, typed := crudFakes(t, nil)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts),
		"my-db", "--version", "16.8.0", "--standbys", "1", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Dry run", "are valid", "1 standby", "Checked locally only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// Nothing may be written, and no request may be sent that the webhook
	// would reject.
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("no DBCluster create should be attempted without the secret")
		}
	}
	if _, err := typed.CoreV1().Secrets(testNamespace).
		Get(context.TODO(), "db-pw-my-db", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("--dry-run must not create the secret, Get returned %v", err)
	}
}

func TestClusterCreateDryRunWithSecretValidatesServerSide(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: testNamespace},
		Data:       map[string][]byte{"my-db": []byte("pw")},
	}
	opts, dyn, _ := crudFakes(t, nil, existing)
	out, err := runCRUDCmd(t, newClusterCreateCommand(opts), "my-db", "--version", "16.8.0", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "accepted by the API server") {
		t.Fatalf("with the secret present the server should be asked:\n%s", out)
	}
	// The request has to actually go out; that is where the webhooks run.
	var sent bool
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" {
			sent = true
		}
	}
	if !sent {
		t.Fatal("expected a dry-run DBCluster create to be sent")
	}
	if strings.Contains(out, "is DBClusterReady") {
		t.Fatalf("--dry-run should not wait:\n%s", out)
	}
}

func TestClusterCreateWaitIgnoresATransientIncident(t *testing.T) {
	// Omni posts transient incidents during a stop/start cycle and then
	// recovers, so one sighting must not end the wait. Here the incident is
	// visible on the first poll only; the wait should run on to its deadline
	// rather than reporting a failure.
	shortIncidentPolling(t)
	opts, dyn, _ := crudFakes(t, []runtime.Object{testDBCluster("my-db", "DBClusterReconciling")})
	opts.Timeout = 300 * time.Millisecond

	failing := testDBCluster("my-db", "DBClusterReconciling")
	_ = unstructured.SetNestedSlice(failing.Object, []any{
		map[string]any{"code": "DBSE0001", "message": "Internal. General Controller Error."},
	}, "status", "criticalIncidents")

	var gets int
	dyn.PrependReactor("get", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, failing, nil
		}
		return true, testDBCluster("my-db", "DBClusterReconciling"), nil
	})

	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterReadyPhase)
	if err == nil {
		t.Fatal("the cluster never became ready, so the wait should still fail")
	}
	if strings.Contains(err.Error(), "DBSE0001") {
		t.Fatalf("a one-off incident must not end the wait: %v", err)
	}
	if !strings.Contains(err.Error(), "did not reach") {
		t.Fatalf("expected the deadline to be the reason, got: %v", err)
	}
}
