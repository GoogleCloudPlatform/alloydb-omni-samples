package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func TestClientsUsesFactory(t *testing.T) {
	want := &k8s.Clients{Namespace: "test"}
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) { return want, nil },
	}
	got, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Clients() = %p, want injected %p", got, want)
	}
}

func TestClientsRequiresConfig(t *testing.T) {
	if _, err := (&GlobalOptions{}).Clients(); err == nil {
		t.Fatal("Clients() with no factory and no kubeconfig should fail")
	}
}

func TestClusterName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	opts := fakeOpts(output.OutputTable)
	name, err := opts.ClusterName([]string{"explicit"})
	if err != nil || name != "explicit" {
		t.Fatalf("positional: got %q, %v", name, err)
	}
	if _, err := opts.ClusterName(nil); err == nil {
		t.Fatal("ClusterName with nothing selected should fail")
	}
	saveTestSelection(t, "test-ctx", "default", "saved")
	name, err = opts.ClusterName(nil)
	if err != nil || name != "saved" {
		t.Fatalf("selected: got %q, %v", name, err)
	}
}

func TestClusterNameReportsCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "adbo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adbo", "config.yaml"), []byte("selectedClusters: [bad]"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fakeOpts(output.OutputTable).ClusterName(nil)
	if err == nil || strings.Contains(err.Error(), "no cluster specified") {
		t.Fatalf("want the config error, not the generic hint; got %v", err)
	}
}

func TestClientsAreBuiltOnce(t *testing.T) {
	calls := 0
	opts := &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		calls++
		return &k8s.Clients{}, nil
	}}
	_, _ = opts.Clients()
	_, _ = opts.Clients()
	if calls != 1 {
		t.Fatalf("NewClients called %d times, want 1", calls)
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := (&GlobalOptions{Timeout: 10 * time.Millisecond}).WithTimeout(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 50*time.Millisecond {
		t.Fatal("expected a short deadline")
	}

	plain, stop := (&GlobalOptions{}).WithTimeout(context.Background())
	defer stop()
	if _, ok := plain.Deadline(); ok {
		t.Fatal("Timeout <= 0 must not set a deadline")
	}
}

func TestConfirm(t *testing.T) {
	cmd := func(in string) *cobra.Command {
		c := &cobra.Command{}
		c.SetIn(strings.NewReader(in))
		c.SetErr(&bytes.Buffer{})
		return c
	}
	ok, err := (&GlobalOptions{Yes: true}).Confirm(cmd(""), "Delete?")
	if err != nil || !ok {
		t.Fatalf("-y: got %v, %v", ok, err)
	}
	ok, err = (&GlobalOptions{}).Confirm(cmd("yes\n"), "Delete?")
	if err != nil || !ok {
		t.Fatalf("yes: got %v, %v", ok, err)
	}
	ok, err = (&GlobalOptions{}).Confirm(cmd("\n"), "Delete?")
	if err != nil || ok {
		t.Fatalf("empty: got %v, %v", ok, err)
	}
}
