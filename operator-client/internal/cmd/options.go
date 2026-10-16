package cmd

import (
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// GlobalOptions holds every flag that works with all commands.
type GlobalOptions struct {
	KubeFlags *genericclioptions.ConfigFlags // kubectl's flags: -n, --context, --kubeconfig
	Timeout time.Duration					// --timeout: max wait for async operations
	Output string							// -o: table | json | yaml
	Yes bool								// -y: skip confirmation prompts
}