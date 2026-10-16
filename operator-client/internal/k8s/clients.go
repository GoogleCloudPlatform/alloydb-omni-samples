package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)


type Clients struct {
	Dynamic dynamic.Interface		// for CRDs
	Typed kubernetes.Interface		// for builtins
	REST *rest.Config

	Namespace string
	Context string
}

// New returns the parsed global flags into ready-to-use clients.
func New(flags *genericclioptions.ConfigFlags) (*Clients, error) {
	restCfg, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}

	loader := flags.ToRawKubeConfigLoader()
	ns, _, err := loader.Namespace() // -n if given, else the context's namespace, else "default"
	if err != nil {
		return nil, err
	}

	raw, err := loader.RawConfig() // the parsed kubeconfig file itself
	if err != nil {
		return nil, err
	}

	ctxName := raw.CurrentContext
	if flags.Context != nil && *flags.Context != "" { // --context overrides the file's current context
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
	return &Clients{Dynamic: dyn, Typed: typed, REST: restCfg, Namespace: ns, Context: ctxName}, nil
}

func (c *Clients) In(gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	return c.Dynamic.Resource(gvr).Namespace(c.Namespace)
}