package cmd

import (
	"bytes"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func backupCR(name, cluster, backupType string) *unstructured.Unstructured {
	spec := map[string]any{"dbclusterRef": cluster}
	if backupType != "" {
		spec["physicalBackupSpec"] = map[string]any{"backupType": backupType}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": "adbo-eugene"},
		"spec":       spec,
	}}
}

func TestRunBackupListFiltersCluster(t *testing.T) {
	opts := &GlobalOptions{
		Output: output.OutputTable,
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(
				backupCR("mine-1", "my-db", "incr"),
				backupCR("other-1", "other", "full"),
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
	if !strings.Contains(s, "mine-1") || !strings.Contains(s, "TYPE") || !strings.Contains(s, "incr") {
		t.Fatalf("missing own backup or type: %q", s)
	}
	if strings.Contains(s, "other-1") || strings.Contains(s, "full") {
		t.Fatalf("leaked other cluster: %q", s)
	}
}

func TestRunBackupListPlanColumnsAndSummary(t *testing.T) {
	plan := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "plan-a", "namespace": "adbo-eugene"},
		"spec": map[string]any{
			"dbclusterRef":     "my-db",
			"backupRetainDays": int64(14),
			"backupLocation":   map[string]any{"type": "GCS"},
			"backupSchedules":  map[string]any{"full": "0 0 * * 0", "incremental": "0 21 * * *"},
		},
		"status": map[string]any{"recoveryWindow": map[string]any{
			"begin": "2026-11-01T00:00:00Z",
			"end":   "2026-11-17T00:00:00Z",
		}},
	}}
	mine := backupCR("mine-1", "my-db", "incr")
	mine.Object["spec"].(map[string]any)["backupPlanRef"] = "plan-a"
	mine.Object["status"] = map[string]any{
		"phase":        "Succeeded",
		"createTime":   "2026-11-17T01:00:00Z",
		"completeTime": "2026-11-17T01:05:00Z",
		"physicalBackupStatus": map[string]any{
			"backupID": "bk-1",
		},
	}
	other := backupCR("other-plan-backup", "my-db", "full")
	other.Object["spec"].(map[string]any)["backupPlanRef"] = "plan-b"
	opts := &GlobalOptions{
		Output: output.OutputTable,
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(plan, mine, other), nil
		},
	}
	cmd := newBackupListCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"my-db", "--plan", "plan-a"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"bk-1", "STARTED", "2026-11-17T01:00:00Z", "2026-11-17T01:05:00Z", "GCS", "plan plan-a", "retain 14 days", "recovery 2026-11-01T00:00:00Z..2026-11-17T00:00:00Z"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %q", want, s)
		}
	}
	if strings.Contains(s, "other-plan-backup") {
		t.Fatalf("plan filter leaked: %q", s)
	}
}

func TestRunBackupListJSONOmitsPlanSummary(t *testing.T) {
	plan := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "plan-a", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": "my-db", "backupRetainDays": int64(14)},
	}}
	mine := backupCR("mine-1", "my-db", "incr")
	mine.Object["spec"].(map[string]any)["backupPlanRef"] = "plan-a"
	opts := &GlobalOptions{
		Output: output.OutputJSON,
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(plan, mine), nil
		},
	}
	cmd := newBackupListCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"my-db"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `"kind": "Backup"`) || strings.Contains(s, "retain 14 days") {
		t.Fatalf("json should be the backup list only: %s", s)
	}
}

func TestRunBackupListRequiresCluster(t *testing.T) {
	cmd := newBackupListCommand(&GlobalOptions{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected missing cluster to fail")
	}
}
