// Package k8s wraps client-go and the AlloyDB Omni GVRs used by adbo commands.
package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Clients is the set of Kubernetes API clients a command needs.
type Clients struct {
	Dynamic dynamic.Interface    // CRDs (DBCluster, Backup, ...)
	Typed   kubernetes.Interface // built-ins (Pods, Secrets, ...)
	REST    *rest.Config

	Namespace         string // -n, else the context namespace, else "default"
	NamespaceExplicit bool   // true when the user passed -n
	Context           string
}

// New builds clients from kubeconfig flags.
func New(flags *genericclioptions.ConfigFlags) (*Clients, error) {
	restCfg, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	// client-go defaults (QPS 5 / Burst 10) throttle multi-resource commands
	// such as status, doctor, and support export.
	restCfg.QPS = 50
	restCfg.Burst = 100

	loader := flags.ToRawKubeConfigLoader()
	ns, explicit, err := loader.Namespace()
	if err != nil {
		return nil, err
	}

	var ctxName string
	if raw, err := loader.RawConfig(); err == nil {
		ctxName = raw.CurrentContext
	}
	if flags.Context != nil && *flags.Context != "" {
		ctxName = *flags.Context
	}

	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return &Clients{
		Dynamic: dyn, Typed: typed, REST: restCfg,
		Namespace: ns, NamespaceExplicit: explicit, Context: ctxName,
	}, nil
}

// In is the current namespace (from -n or kubeconfig).
func (c *Clients) In(gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	return c.InNamespace(gvr, c.Namespace)
}

// InNamespace returns a namespaced client. Pass "" (metav1.NamespaceAll)
// to list across every namespace, same as kubectl -A.
func (c *Clients) InNamespace(gvr schema.GroupVersionResource, ns string) dynamic.ResourceInterface {
	if ns == "" {
		return c.Dynamic.Resource(gvr)
	}
	return c.Dynamic.Resource(gvr).Namespace(ns)
}

// Cluster is for cluster-scoped types: CRDs, Nodes, webhooks, StorageClasses.
func (c *Clients) Cluster(gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	return c.Dynamic.Resource(gvr)
}
