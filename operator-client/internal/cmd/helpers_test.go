package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/config"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

func dbCluster(ns, name, phase string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"databaseVersion": "16.8.0"},
	}}
	u.SetAPIVersion(k8s.APIVersion)
	u.SetKind("DBCluster")
	u.SetNamespace(ns)
	u.SetName(name)
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
		_ = unstructured.SetNestedField(u.Object, "Ready", "status", "primary", "phase")
		_ = unstructured.SetNestedField(u.Object, "10.0.0.1", "status", "primary", "endpoint")
		_ = unstructured.SetNestedSlice(u.Object, []any{
			map[string]any{"type": "Ready", "status": "False"},
			map[string]any{"type": "HAReady", "status": "True"},
		}, "status", "conditions")
	}
	return u
}

// fakeOpts returns options whose clients talk to a fake API server seeded
// with objs, in namespace "default" of context "test-ctx".
func fakeOpts(format string, objs ...runtime.Object) *GlobalOptions {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.DBClusterGVR: "DBClusterList"}, objs...)
	return &GlobalOptions{
		Output: format,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Dynamic: dyn, Namespace: "default", Context: "test-ctx"}, nil
		},
	}
}

// testCommand returns a bare command that writes to buf.
func testCommand(buf *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	return cmd
}

// saveTestSelection writes a selection to the config file under XDG_CONFIG_HOME.
func saveTestSelection(t *testing.T, kubeContext, namespace, name string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSelectedCluster(kubeContext, namespace, name)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}
