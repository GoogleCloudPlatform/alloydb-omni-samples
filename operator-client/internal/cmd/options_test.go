package cmd

import (
	"testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
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
	opts := &GlobalOptions{SelectedCluster: "saved"}
	name, err := opts.ClusterName([]string{"explicit"})
	if err != nil || name != "explicit" {
		t.Fatalf("positional: got %q, %v", name, err)
	}
	name, err = opts.ClusterName(nil)
	if err != nil || name != "saved" {
		t.Fatalf("selected: got %q, %v", name, err)
	}
	if _, err := (&GlobalOptions{}).ClusterName(nil); err == nil {
		t.Fatal("empty ClusterName should fail")
	}
}
