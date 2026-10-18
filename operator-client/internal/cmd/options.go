package cmd

import (
	"fmt"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// GlobalOptions holds flags shared by every command. Pass the same pointer
// into each newXxxCommand so a flag set on root is visible everywhere.
type GlobalOptions struct {
	KubeFlags *genericclioptions.ConfigFlags // -n, --context, --kubeconfig, ...
	Timeout   time.Duration                  // --timeout: max wait for async ops
	Output    string                         // -o: table | json | yaml
	Yes       bool                           // -y: skip confirmation prompts

	// NewClients is nil in production. Tests replace it with a fake so
	// runXxx can be exercised without a kubeconfig or live cluster.
	NewClients func() (*k8s.Clients, error)

	// SelectedCluster is the default from `adbo cluster select`. Tests
	// set this directly; persistence lands with that command.
	SelectedCluster string
}

// Clients returns Kubernetes clients. Production uses kubeconfig;
// tests inject fakes via NewClients.
func (o *GlobalOptions) Clients() (*k8s.Clients, error) {
	if o.NewClients != nil {
		return o.NewClients()
	}
	if o.KubeFlags == nil {
		return nil, fmt.Errorf("kubernetes clients not configured")
	}
	return k8s.New(o.KubeFlags)
}

// ClusterName prefers a positional argument, then the cluster saved by
// `adbo cluster select`. Commands with an optional [CLUSTER_NAME] should
// call this instead of reading args themselves.
func (o *GlobalOptions) ClusterName(args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if o.SelectedCluster != "" {
		return o.SelectedCluster, nil
	}
	if name := loadSelectedCluster(); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("no cluster specified: pass CLUSTER_NAME or run `adbo cluster select CLUSTER_NAME`")
}

// loadSelectedCluster reads the default cluster from local config.
// Persistence should be keyed by kube-context + namespace so a context
// switch cannot silently retarget another cluster.
func loadSelectedCluster() string {
	// TODO: read ~/.config/adbo/config.yaml once `cluster select` lands.
	return ""
}
