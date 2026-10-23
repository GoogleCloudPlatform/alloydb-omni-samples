package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestNamespaceHelpers(t *testing.T) {
	c := &Clients{
		Dynamic:   dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		Namespace: "default",
	}
	if c.In(BackupGVR) == nil {
		t.Fatal("In returned nil")
	}
	if c.InNamespace(BackupGVR, "") == nil {
		t.Fatal("InNamespace(\"\") returned nil")
	}
	if c.Cluster(BackupGVR) == nil {
		t.Fatal("Cluster returned nil")
	}
}
