package cmd

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// getDBCluster fetches a DBCluster. FormatErr says where adbo looked and
// tells a missing object apart from a missing CRD (operator not installed).
func getDBCluster(ctx context.Context, c *k8s.Clients, name string) (*unstructured.Unstructured, error) {
	obj, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, c.FormatErr(fmt.Errorf("getting dbcluster %q: %w", name, err))
	}
	return obj, nil
}
