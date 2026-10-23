package cmd

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// getDBCluster fetches a DBCluster. A NotFound error says where adbo looked,
// since the usual cause is the wrong namespace or kube context, or that the
// operator isn't installed at all.
func getDBCluster(ctx context.Context, c *k8s.Clients, name string) (*unstructured.Unstructured, error) {
	obj, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		inContext := ""
		if c.Context != "" {
			inContext = fmt.Sprintf(" (context %q)", c.Context)
		}
		if !dbclusterTypeServed(c) {
			return nil, fmt.Errorf("this Kubernetes cluster%s has no DBCluster resource type; is the AlloyDB Omni operator installed?", inContext)
		}
		return nil, fmt.Errorf("dbcluster %q not found in namespace %q%s; pass -n NAMESPACE or list clusters with `kubectl get dbclusters -A`", name, c.Namespace, inContext)
	}
	if err != nil {
		return nil, c.FormatErr(fmt.Errorf("getting dbcluster %q: %w", name, err))
	}
	return obj, nil
}

// dbclusterTypeServed asks discovery whether the DBCluster CRD is installed.
// A missing object and a missing CRD both come back as NotFound, with the
// same name in the details. When discovery fails for another reason, assume
// the type is served so the namespace hint is shown.
func dbclusterTypeServed(c *k8s.Clients) bool {
	list, err := c.Typed.Discovery().ServerResourcesForGroupVersion(k8s.DBClusterGVR.GroupVersion().String())
	if err != nil {
		return !apierrors.IsNotFound(err)
	}
	for _, r := range list.APIResources {
		if r.Name == k8s.DBClusterGVR.Resource {
			return true
		}
	}
	return false
}
