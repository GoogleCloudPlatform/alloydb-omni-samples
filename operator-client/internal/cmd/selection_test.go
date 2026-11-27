package cmd

import (
	"testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func TestPrepareClusterUsesSelectedNamespace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "test-ctx", "adbo-eugene", "my-db")
	opts := fakeOpts(output.OutputTable)
	name, c, err := prepareCluster(opts, []string{"my-db"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-db" || c.Namespace != "adbo-eugene" {
		t.Fatalf("got %s in %s", name, c.Namespace)
	}
}

func TestPrepareClusterWithoutNameUsesOnlySelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "test-ctx", "adbo-eugene", "my-db")
	opts := fakeOpts(output.OutputTable)
	name, c, err := prepareCluster(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-db" || c.Namespace != "adbo-eugene" {
		t.Fatalf("got %s in %s", name, c.Namespace)
	}
}

func TestPrepareClusterExplicitNamespaceWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "test-ctx", "adbo-eugene", "my-db")
	opts := fakeOpts(output.OutputTable)
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	c.Namespace = "other"
	c.NamespaceExplicit = true
	name, got, err := prepareCluster(opts, []string{"my-db"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-db" || got.Namespace != "other" {
		t.Fatalf("got %s in %s", name, got.Namespace)
	}
}

func TestFollowSelectedNamespace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	saveTestSelection(t, "test-ctx", "adbo-eugene", "my-db")
	c := &k8s.Clients{Context: "test-ctx", Namespace: "default"}
	if err := followSelectedNamespace(c); err != nil {
		t.Fatal(err)
	}
	if c.Namespace != "adbo-eugene" {
		t.Fatalf("namespace %s", c.Namespace)
	}
}
