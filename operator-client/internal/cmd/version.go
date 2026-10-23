package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version"
)

// The operator's Helm chart installs the fleet controller here by default.
// Other installs are found by the Deployment label.
const (
	operatorNamespace  = "alloydb-omni-system"
	operatorDeployment = "fleet-controller-manager"
	operatorSelector   = "fleet-control-plane=controller-manager"
	operatorImageName  = "fleet-operator"
)

// operatorLookupTimeout keeps `adbo version` fast when the cluster is unreachable.
const operatorLookupTimeout = 5 * time.Second

// OperatorInfo is the AlloyDB Omni operator found in the current kube context.
// Installed is unset when the lookup failed; Error then says why.
type OperatorInfo struct {
	Installed *bool  `json:"installed,omitempty"`
	Version   string `json:"version,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Image     string `json:"image,omitempty"`
	Error     string `json:"error,omitempty"`
}

// VersionInfo is what `adbo version -o json|yaml` prints.
type VersionInfo struct {
	Client   version.Info  `json:"client"`
	Operator *OperatorInfo `json:"operator,omitempty"`
}

func newVersionCommand(opts *GlobalOptions) *cobra.Command {
	var clientOnly bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the adbo and AlloyDB Omni operator versions",
		Example: "  adbo version\n" +
			"  adbo version --client\n" +
			"  adbo version -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(cmd, opts, clientOnly)
		},
	}

	cmd.Flags().BoolVar(&clientOnly, "client", false, "only show the adbo version; don't contact the cluster")
	return cmd
}

func runVersion(cmd *cobra.Command, opts *GlobalOptions, clientOnly bool) error {
	info := VersionInfo{Client: version.Get()}

	client := info.Client.Version
	if info.Client.GitCommit != "" {
		client += " (" + info.Client.GitCommit + ")"
	}
	table := output.Table{
		Headers: []string{"COMPONENT", "VERSION"},
		Rows:    [][]string{{"client", client}},
	}

	if !clientOnly {
		op, err := findOperator(cmd.Context(), opts)
		switch {
		case err != nil:
			// The client version is still useful without a cluster, so warn
			// instead of failing.
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not determine the operator version: %v\n", err)
			info.Operator = &OperatorInfo{Error: err.Error()}
			table.Rows = append(table.Rows, []string{"operator", "unknown"})
		case !*op.Installed:
			info.Operator = op
			table.Rows = append(table.Rows, []string{"operator", "not installed"})
		default:
			info.Operator = op
			table.Rows = append(table.Rows, []string{"operator", op.Version})
		}
	}

	return output.Print(cmd.OutOrStdout(), opts.Output, table, info)
}

// findOperator reads the version from the operator Deployment's image tag.
// It tries the default install first, then searches every namespace.
func findOperator(ctx context.Context, opts *GlobalOptions) (*OperatorInfo, error) {
	c, err := opts.Clients()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, operatorLookupTimeout)
	defer cancel()

	deps := c.Typed.AppsV1()
	d, err := deps.Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
	if err == nil {
		return operatorFrom(d), nil
	}
	if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, err
	}

	list, err := deps.Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: operatorSelector})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		installed := false
		return &OperatorInfo{Installed: &installed}, nil
	}
	return operatorFrom(&list.Items[0]), nil
}

func operatorFrom(d *appsv1.Deployment) *OperatorInfo {
	installed := true
	info := &OperatorInfo{Installed: &installed, Version: "unknown", Namespace: d.Namespace}
	for _, c := range d.Spec.Template.Spec.Containers {
		if strings.Contains(c.Image, "/"+operatorImageName) {
			info.Image = c.Image
			info.Version = imageTag(c.Image)
			break
		}
	}
	return info
}

// imageTag returns the tag of an image reference, else a short digest if the
// image is pinned by digest only, else "latest".
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		digest := image[i+1:]
		if tag := imageTag(image[:i]); tag != "latest" {
			return tag
		}
		if len(digest) > len("sha256:")+12 {
			digest = digest[:len("sha256:")+12]
		}
		return digest
	}
	// A ':' before the last '/' belongs to the registry port, not the tag.
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return "latest"
}
