package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// runList runs `cluster list` against a fake API server seeded with objs.
func runList(t *testing.T, format string, allNamespaces bool, objs ...runtime.Object) string {
	t.Helper()
	var buf bytes.Buffer
	if err := runClusterList(testCommand(&buf), fakeOpts(format, objs...), allNamespaces); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestClusterListCurrentNamespace(t *testing.T) {
	out := runList(t, output.OutputTable, false,
		dbCluster("default", "b-db", "DBClusterReady"),
		dbCluster("default", "a-db", ""),
		dbCluster("prod", "other", "DBClusterReady"),
	)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got:\n%s", out)
	}
	if strings.Contains(out, "NAMESPACE") || strings.Contains(out, "other") {
		t.Fatalf("should only show namespace default:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "a-db") || !strings.HasPrefix(lines[2], "b-db") {
		t.Fatalf("rows not sorted by name:\n%s", out)
	}
	for _, want := range []string{"DBClusterReady", "10.0.0.1", "16.8.0", "True"} {
		if !strings.Contains(lines[2], want) {
			t.Fatalf("row missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(lines[1], " - ") {
		t.Fatalf("unset status should render as -:\n%s", out)
	}
}

func TestClusterListAllNamespaces(t *testing.T) {
	out := runList(t, output.OutputTable, true,
		dbCluster("prod", "db", "DBClusterReady"),
		dbCluster("default", "db", "DBClusterReady"),
	)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAMESPACE") {
		t.Fatalf("want NAMESPACE column and 2 rows:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "default") || !strings.HasPrefix(lines[2], "prod") {
		t.Fatalf("rows not sorted by namespace:\n%s", out)
	}
}

func TestClusterListEmpty(t *testing.T) {
	if out := runList(t, output.OutputTable, false); !strings.Contains(out, "(none)") {
		t.Fatalf("empty table should say (none):\n%s", out)
	}
	if out := strings.TrimSpace(runList(t, output.OutputJSON, false)); out != "[]" {
		t.Fatalf("empty json should be [], got %s", out)
	}
}

func TestClusterListJSON(t *testing.T) {
	out := runList(t, output.OutputJSON, false, dbCluster("default", "db", "DBClusterReady"))
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got) != 1 || got[0]["kind"] != "DBCluster" {
		t.Fatalf("want raw DBCluster object, got:\n%s", out)
	}
}

func TestClusterListRejectsArgs(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"cluster", "list", "extra"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("expected positional arg to be rejected")
	}
}
