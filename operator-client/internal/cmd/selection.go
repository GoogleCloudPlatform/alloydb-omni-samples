package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/config"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// prepareCluster resolves the cluster name and, when -n was not passed,
// the namespace saved by `adbo cluster select` for this kube-context.
// The returned clients are the cached ones, already pointed at that namespace.
func prepareCluster(opts *GlobalOptions, args []string) (string, *k8s.Clients, error) {
	c, err := opts.Clients()
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return "", nil, err
	}
	name, err := selectedClusterName(opts, c, cfg, args)
	if err != nil {
		return "", nil, err
	}
	if err := useSelectedNamespace(c, cfg, name); err != nil {
		return "", nil, err
	}
	return name, c, nil
}

func selectedClusterName(opts *GlobalOptions, c *k8s.Clients, cfg *config.Config, args []string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		return args[0], nil
	}
	if opts.SelectedCluster != "" {
		return opts.SelectedCluster, nil
	}
	if name := cfg.SelectedCluster(c.Context, c.Namespace); name != "" {
		return name, nil
	}
	if !c.NamespaceExplicit {
		if name, ns, ok := onlySelection(cfg, c.Context); ok {
			c.Namespace = ns
			return name, nil
		}
	}
	return "", fmt.Errorf("no cluster specified: pass CLUSTER_NAME or run `adbo cluster select CLUSTER_NAME`")
}

// useSelectedNamespace moves c to the namespace where name was selected
// in this kube-context. An explicit -n wins. A cluster that was not
// selected stays in the current namespace.
func useSelectedNamespace(c *k8s.Clients, cfg *config.Config, name string) error {
	if c == nil || c.NamespaceExplicit || name == "" || cfg == nil {
		return nil
	}
	if cfg.SelectedCluster(c.Context, c.Namespace) == name {
		return nil
	}
	var matches []string
	for ns, selected := range cfg.SelectedClusters[c.Context] {
		if selected == name {
			matches = append(matches, ns)
		}
	}
	switch len(matches) {
	case 0:
		return nil
	case 1:
		c.Namespace = matches[0]
		return nil
	default:
		sort.Strings(matches)
		return fmt.Errorf("cluster %q is selected in namespaces %s; pass -n", name, strings.Join(matches, ", "))
	}
}

// followSelectedNamespace points c at the only namespace selected in this
// kube-context. Commands without a cluster argument, such as doctor, use it
// so a prior `cluster select -n` still applies.
func followSelectedNamespace(c *k8s.Clients) error {
	if c == nil || c.NamespaceExplicit {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.SelectedCluster(c.Context, c.Namespace) != "" {
		return nil
	}
	if _, ns, ok := onlySelection(cfg, c.Context); ok {
		c.Namespace = ns
	}
	return nil
}

func onlySelection(cfg *config.Config, kubeContext string) (name, namespace string, ok bool) {
	selected := cfg.SelectedClusters[kubeContext]
	if len(selected) != 1 {
		return "", "", false
	}
	for ns, clusterName := range selected {
		return clusterName, ns, true
	}
	return "", "", false
}
