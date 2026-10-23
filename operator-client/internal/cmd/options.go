package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// GlobalOptions holds flags shared by every command. Pass the same pointer
// into each newXxxCommand so a flag set on root is visible everywhere.
type GlobalOptions struct {
	KubeFlags *genericclioptions.ConfigFlags // -n, --context, --kubeconfig, ...
	Timeout   time.Duration                  // --timeout: max wait for --wait ops only
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

// WithTimeout bounds ctx for async waits (backup trigger, create, restore).
// Do not wrap cmd.Context() globally — that would kill connect/logs at 30m.
func (o *GlobalOptions) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.Timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, o.Timeout)
}

// Confirm asks "prompt [y/N] " unless -y was set.
// The question goes to cmd's error stream so piped stdout stays clean.
// The answer comes from cmd's input so tests can SetIn instead of touching the real terminal.
func (o *GlobalOptions) Confirm(cmd *cobra.Command, prompt string) (bool, error) {
	if o.Yes {
		return true, nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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
	if name := o.loadSelectedCluster(); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("no cluster specified: pass CLUSTER_NAME or run `adbo cluster select CLUSTER_NAME`")
}

// loadSelectedCluster reads the default cluster from local config.
// Persistence should be keyed by kube-context + namespace so a context
// switch cannot silently retarget another cluster.
func (o *GlobalOptions) loadSelectedCluster() string {
	if o.SelectedCluster != "" {
		return o.SelectedCluster
	}
	// TODO: ~/.config/adbo/config.yaml keyed by context + namespace
	return ""
}
