package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

const (
	testNamespace = "db"
	testContext   = "omni-vm"
)

// crudFakes wires a dynamic fake (DBClusters) and a typed fake (Secrets) into
// GlobalOptions. fakeClients in backup_trigger_test.go sets only Dynamic;
// delete also needs Typed for the db-pw-<name> secret, and these tests need
// the fakes back to add reactors and assert on what was written.
func crudFakes(t *testing.T, objs []runtime.Object, secrets ...runtime.Object) (*GlobalOptions, *dynamicfake.FakeDynamicClient, *typedfake.Clientset) {
	t.Helper()
	scheme := runtime.NewScheme()
	gv := k8s.DBClusterGVR.GroupVersion()
	scheme.AddKnownTypeWithName(gv.WithKind("DBCluster"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind("DBClusterList"), &unstructured.UnstructuredList{})

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{k8s.DBClusterGVR: "DBClusterList"}, objs...)
	typed := typedfake.NewSimpleClientset(secrets...)

	opts := &GlobalOptions{
		Output:  output.OutputTable,
		Yes:     true,
		Timeout: 30 * time.Second,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Dynamic: dyn, Typed: typed, Namespace: testNamespace, Context: testContext}, nil
		},
	}
	return opts, dyn, typed
}

// testDBCluster is a minimal DBCluster at the given phase. Named apart from
// the dbcluster() helper in backup_trigger_test.go so both can coexist.
func testDBCluster(name, phase string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBCluster",
		"metadata":   map[string]any{"name": name, "namespace": testNamespace},
		"spec":       map[string]any{"databaseVersion": "16.8.0"},
	}}
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

// runCRUDCmd executes a built command with args, returning combined output.
func runCRUDCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// shortPolling makes the delete waiter spin fast so tests stay quick.
func shortPolling(t *testing.T) {
	t.Helper()
	prev := deleteGonePollInterval
	deleteGonePollInterval = time.Millisecond
	t.Cleanup(func() { deleteGonePollInterval = prev })
}

// adminSecretFor is the db-pw-<cluster> secret create would have made.
func adminSecretFor(cluster string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-" + cluster, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{cluster: []byte("pw")},
	}
}

func clusterExists(t *testing.T, opts *GlobalOptions, name string) bool {
	t.Helper()
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Dynamic.Resource(k8s.DBClusterGVR).Namespace(testNamespace).
		Get(context.TODO(), name, metav1.GetOptions{})
	return err == nil
}

func secretExists(t *testing.T, opts *GlobalOptions, cluster string) bool {
	t.Helper()
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Typed.CoreV1().Secrets(testNamespace).
		Get(context.TODO(), "db-pw-"+cluster, metav1.GetOptions{})
	return err == nil
}

func TestClusterDeleteRemovesClusterAndSecret(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if clusterExists(t, opts, "my-db") {
		t.Fatal("DBCluster should have been deleted")
	}
	if secretExists(t, opts, "my-db") {
		t.Fatal("admin secret should have been deleted")
	}
	for _, want := range []string{`Deleting cluster "my-db"`, `Deleted the admin secret "db-pw-my-db"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestClusterDeleteKeepSecret(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--keep-secret")
	if err != nil {
		t.Fatal(err)
	}
	if clusterExists(t, opts, "my-db") {
		t.Fatal("DBCluster should still have been deleted")
	}
	if !secretExists(t, opts, "my-db") {
		t.Fatal("--keep-secret should have left the admin secret in place")
	}
	if !strings.Contains(out, `Kept the admin secret "db-pw-my-db"`) {
		t.Fatalf("output should say the secret was kept:\n%s", out)
	}
}

func TestClusterDeleteAsksForConfirmation(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))
	opts.Yes = false // force the prompt

	cmd := newClusterDeleteCommand(opts)
	cmd.SetIn(strings.NewReader("n\n"))
	out, err := runCRUDCmd(t, cmd, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Cancelled; nothing deleted.") {
		t.Fatalf("answering no should cancel:\n%s", out)
	}
	if !clusterExists(t, opts, "my-db") {
		t.Fatal("a cancelled delete must not remove the cluster")
	}
	if !secretExists(t, opts, "my-db") {
		t.Fatal("a cancelled delete must not remove the secret")
	}
	// The prompt should name what is about to be destroyed.
	for _, want := range []string{"my-db", testNamespace, "db-pw-my-db", "cannot be undone"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestClusterDeleteNotFoundNamesNamespaceAndContext(t *testing.T) {
	opts, _, _ := crudFakes(t, nil)
	_, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "missing-db")
	if err == nil {
		t.Fatal("deleting a missing cluster should fail")
	}
	for _, want := range []string{`"missing-db"`, `"db"`, `"omni-vm"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("not-found error missing %s: %v", want, err)
		}
	}
}

func TestClusterDeleteMissingSecretIsNotAnError(t *testing.T) {
	// Clusters created by hand may have no db-pw- secret at all.
	opts, _, _ := crudFakes(t, []runtime.Object{testDBCluster("my-db", "DBClusterReady")})
	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `No admin secret "db-pw-my-db" to delete.`) {
		t.Fatalf("a missing secret should be reported, not fatal:\n%s", out)
	}
}

func TestClusterDeleteWaitsUntilGone(t *testing.T) {
	shortPolling(t)
	opts, dyn, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	// The object lingers while finalizers run, then disappears. Proves --wait
	// polls for NotFound rather than trusting the Delete call.
	gets := 0
	dyn.PrependReactor("get", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		switch gets {
		case 1, 2: // the pre-flight read, then one still-terminating poll
			return true, testDBCluster("my-db", "DBClusterDeleting"), nil
		}
		return true, nil, apierrors.NewNotFound(k8s.DBClusterGVR.GroupResource(), "my-db")
	})

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if gets < 3 {
		t.Fatalf("expected --wait to poll after deleting, got %d gets", gets)
	}
	if !strings.Contains(out, `Cluster "my-db" is gone.`) {
		t.Fatalf("--wait should confirm the cluster is gone:\n%s", out)
	}
}

func TestClusterDeleteWaitTimesOut(t *testing.T) {
	shortPolling(t)
	opts, dyn, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))
	opts.Timeout = 30 * time.Millisecond

	// Finalizers never finish.
	dyn.PrependReactor("get", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, testDBCluster("my-db", "DBClusterDeleting"), nil
	})

	_, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--wait")
	if err == nil {
		t.Fatal("a cluster stuck in deletion should time out")
	}
	for _, want := range []string{"timed out", "finalizers", "my-db"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout error missing %q: %v", want, err)
		}
	}
}

func TestClusterDeleteWaitsByDefault(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `Cluster "my-db" is gone.`) {
		t.Fatalf("delete should wait for the cluster to go by default:\n%s", out)
	}
}

func TestClusterDeleteNoWaitReturnsEarly(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Not waiting for the operator to finish") {
		t.Fatalf("--no-wait should skip the wait:\n%s", out)
	}
}

func TestClusterDeleteRejectsBothWaitFlags(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))
	if _, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--wait", "--no-wait"); err == nil {
		t.Fatal("--wait and --no-wait are mutually exclusive")
	}
}

func TestClusterGroupRegistersDelete(t *testing.T) {
	for _, c := range newClusterCommand(&GlobalOptions{}).Commands() {
		if c.Name() == "delete" {
			return
		}
	}
	t.Fatal("`adbo cluster delete` should be registered on the cluster group")
}

func TestClusterDeleteDryRunSkipsThePrompt(t *testing.T) {
	// Nothing changes, so there is nothing to confirm.
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))
	opts.Yes = false
	cmd := newClusterDeleteCommand(opts)
	cmd.SetIn(strings.NewReader("")) // no answer available
	out, err := runCRUDCmd(t, cmd, "my-db", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[y/N]") {
		t.Fatalf("--dry-run should not prompt:\n%s", out)
	}
}

func TestClusterDeleteDryRunLeavesEverything(t *testing.T) {
	opts, _, _ := crudFakes(t,
		[]runtime.Object{testDBCluster("my-db", "DBClusterReady")},
		adminSecretFor("my-db"))

	out, err := runCRUDCmd(t, newClusterDeleteCommand(opts), "my-db", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("dry run should say nothing was changed:\n%s", out)
	}
	// It must be honest that the DELETE itself was not validated.
	if !strings.Contains(out, "cannot be validated yet") {
		t.Fatalf("dry run should explain the webhook limitation:\n%s", out)
	}
	if !clusterExists(t, opts, "my-db") {
		t.Fatal("--dry-run must not delete the cluster")
	}
	// The secret delete is ours, not the API server's, so it must be skipped
	// outright rather than relying on a DryRun option.
	if !secretExists(t, opts, "my-db") {
		t.Fatal("--dry-run must not delete the admin secret")
	}
	if strings.Contains(out, "is gone") {
		t.Fatalf("--dry-run should not wait:\n%s", out)
	}
}
