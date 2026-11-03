package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/config"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func TestClusterSelectSavesAndResolves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := fakeOpts(output.OutputTable, dbCluster("default", "my-db", "DBClusterReady"))

	var buf bytes.Buffer
	if err := runClusterSelect(testCommand(&buf), opts, "my-db", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `Selected cluster "my-db"`) {
		t.Fatalf("unexpected output: %s", buf.String())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SelectedCluster("test-ctx", "default"); got != "my-db" {
		t.Fatalf("saved %q, want my-db", got)
	}

	// Commands that omit CLUSTER_NAME now fall back to the selection.
	name, err := opts.ClusterName(nil)
	if err != nil || name != "my-db" {
		t.Fatalf("ClusterName(nil) = %q, %v", name, err)
	}
}

func TestClusterSelectRejectsUnknownCluster(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := fakeOpts(output.OutputTable)

	var buf bytes.Buffer
	err := runClusterSelect(testCommand(&buf), opts, "typo", false)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
	cfg, _ := config.Load()
	if got := cfg.SelectedCluster("test-ctx", "default"); got != "" {
		t.Fatalf("unknown cluster should not be saved, got %q", got)
	}
}

func TestClusterSelectShowAndClear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := fakeOpts(output.OutputTable, dbCluster("default", "my-db", "DBClusterReady"))

	var buf bytes.Buffer
	if err := runClusterShow(testCommand(&buf), opts, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No cluster selected") {
		t.Fatalf("want empty selection message, got: %s", buf.String())
	}

	buf.Reset()
	_ = runClusterSelect(testCommand(&buf), opts, "my-db", false)
	buf.Reset()
	if err := runClusterShow(testCommand(&buf), opts, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "my-db ") {
		t.Fatalf("want current selection, got: %s", buf.String())
	}

	buf.Reset()
	if err := runClusterClear(testCommand(&buf), opts, false); err != nil {
		t.Fatal(err)
	}
	if name, err := opts.ClusterName(nil); err == nil {
		t.Fatalf("selection should be cleared, got %q", name)
	}
}

func TestClusterSelectIsScopedToContextAndNamespace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "other-ctx", "default", "elsewhere")
	if name, err := fakeOpts(output.OutputTable).ClusterName(nil); err == nil {
		t.Fatalf("selection from another context leaked: %q", name)
	}
}

func TestClusterSelectClearRejectsName(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"cluster", "select", "my-db", "--clear"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil {
		t.Fatal("expected --clear with a name to fail")
	}
}

func TestClusterSelectVerbose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	opts := fakeOpts(output.OutputTable, dbCluster("default", "my-db", "DBClusterReady"))

	var buf bytes.Buffer
	if err := runClusterSelect(testCommand(&buf), opts, "my-db", true); err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]string{"Phase:": "DBClusterReady", "Endpoint:": "10.0.0.1", "Version:": "16.8.0", "HA:": "True", "Saved in:": filepath.Join(dir, "adbo", "config.yaml")} {
		if !hasDetail(buf.String(), label, want) {
			t.Errorf("missing %s %s in output:\n%s", label, want, buf.String())
		}
	}

	// Showing the current selection with -v includes the same details.
	buf.Reset()
	if err := runClusterShow(testCommand(&buf), opts, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "my-db ") || !hasDetail(buf.String(), "Phase:", "DBClusterReady") {
		t.Fatalf("unexpected verbose show output:\n%s", buf.String())
	}
}

func TestClusterSelectVerboseWarnsWhenSelectionDeleted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "test-ctx", "default", "gone-db")

	var buf bytes.Buffer
	if err := runClusterShow(testCommand(&buf), fakeOpts(output.OutputTable), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `cluster "gone-db" no longer exists`) {
		t.Fatalf("want deleted-cluster warning, got:\n%s", buf.String())
	}
}

// hasDetail reports whether out has a line "label <spaces> value".
func hasDetail(out, label, value string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(strings.TrimSpace(line), ":", 2); len(f) == 2 &&
			f[0]+":" == label && strings.TrimSpace(f[1]) == value {
			return true
		}
	}
	return false
}

func TestClusterClearVerbosePrintsPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := fakeOpts(output.OutputTable)
	var buf bytes.Buffer
	if err := runClusterClear(testCommand(&buf), opts, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Saved in:") {
		t.Errorf("want config path with -v, got: %s", buf.String())
	}
}
