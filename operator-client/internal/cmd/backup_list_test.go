package cmd

import (
	"bytes"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func backupCR(name, cluster string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": cluster},
	}}
}

func TestRunBackupListFiltersCluster(t *testing.T) {
	opts := &GlobalOptions{
		Output: output.OutputTable,
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(
				backupCR("mine-1", "my-db"),
				backupCR("other-1", "other"),
			), nil
		},
	}
	cmd := newBackupListCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"my-db"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "mine-1") {
		t.Fatalf("missing own backup: %q", s)
	}
	if strings.Contains(s, "other-1") {
		t.Fatalf("leaked other cluster: %q", s)
	}
}

func TestRunBackupListRequiresCluster(t *testing.T) {
	cmd := newBackupListCommand(&GlobalOptions{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected missing cluster to fail")
	}
}
