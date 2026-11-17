package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

func TestRunHealthy(t *testing.T) {
	rep, err := Run(context.Background(), healthyClients(t, nil, nil), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Fatalf("installation %s next %s\n%s", rep.Installation, rep.Next, details(rep))
	}
	assertStatus(t, rep, "kubernetes-api", Pass)
	assertStatus(t, rep, "kubernetes-version", Pass)
	assertStatus(t, rep, "cert-manager-webhook", Pass)
	assertStatus(t, rep, "alloydb-operator", Pass)
	assertStatus(t, rep, "cgroup-v2", Skip)
	assertStatus(t, rep, "versions", Pass)
	assertStatus(t, rep, "storageclass", Pass)
	ver := find(rep, "versions")
	if !strings.Contains(ver.Detail, "specs match") || !strings.Contains(ver.Detail, "client ") {
		t.Fatalf("versions: %s", ver.Detail)
	}
	if got := find(rep, "alloydb-operator").Detail; got != "local:1.2.0, fleet:1.2.0" {
		t.Fatalf("operator detail: %s", got)
	}
	if got := find(rep, "cert-manager-webhook").Detail; got != "ready (cert-manager:v1.14.0, cert-manager-webhook:v1.14.0)" {
		t.Fatalf("cert-manager detail: %s", got)
	}
	if !strings.Contains(rep.Note, "upstream releases were not checked") {
		t.Fatalf("note: %s", rep.Note)
	}
}

func TestRunImagePullIsTheCause(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-webhook-abc", Namespace: "cert-manager"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "webhook",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}},
		}}},
	}
	rep, err := Run(context.Background(), healthyClients(t, nil, []runtime.Object{pod}), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Failed() {
		t.Fatal("expected installation not ready")
	}
	got := find(rep, "cert-manager-webhook")
	if got.Status != Fail || !strings.Contains(got.Detail, "ImagePullBackOff") || !strings.Contains(got.Detail, "cert-manager-webhook-abc") {
		t.Fatalf("detail should name the image pull: %s", got.Detail)
	}
	if !strings.Contains(got.Next, "cert-manager-webhook-abc") {
		t.Fatalf("next: %s", got.Next)
	}
}

func TestRunZeroNodesIsNotAFailedInstall(t *testing.T) {
	rep, err := Run(context.Background(), healthyClients(t, nil, nil, skipNodes), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Fatalf("zero nodes should not fail the install:\n%s", details(rep))
	}
	nodes := find(rep, "nodes")
	if nodes.Status != Warn || !strings.Contains(nodes.Detail, "Autopilot") {
		t.Fatalf("nodes: %+v", nodes)
	}
	if find(rep, "node-resources").Status != Skip {
		t.Fatal("expected node-resources to be skipped")
	}
}

func TestRunVersionMismatchWarns(t *testing.T) {
	cluster := dbCluster()
	cluster.Object["status"].(map[string]any)["primary"].(map[string]any)["currentDatabaseVersion"] = "16.8.0"
	rep, err := Run(context.Background(), healthyClients(t, []runtime.Object{cluster}, nil), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Fatalf("version drift is a warning:\n%s", details(rep))
	}
	got := find(rep, "versions")
	if got.Status != Warn || !strings.Contains(got.Detail, "upgrade in progress or stuck") {
		t.Fatalf("versions: %+v", got)
	}
}

func TestRunAPIError(t *testing.T) {
	c := healthyClients(t, nil, nil)
	c.Typed.(*fake.Clientset).PrependReactor("get", "version", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})
	rep, err := Run(context.Background(), c, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Name != "kubernetes-api" || !rep.Failed() {
		t.Fatalf("%+v", rep.Results)
	}
}

func TestEvaluateSmallNodeFailsResources(t *testing.T) {
	rep := Evaluate(Snapshot{
		ServerGit: "v1.29.0",
		Context:   "test",
		Nodes: []NodeFact{{
			Name:   "small",
			Arch:   "arm64",
			Kernel: "4.17.0",
			CPU:    resource.MustParse("1"),
			Memory: resource.MustParse("2Gi"),
		}},
		CertCRDs: map[string]bool{
			"certificates.cert-manager.io":   true,
			"issuers.cert-manager.io":        true,
			"clusterissuers.cert-manager.io": true,
		},
		CertController:       DepFact{Name: "cert-manager", Found: true, Ready: true, Image: "cm:v1"},
		CertWebhook:          DepFact{Name: "cert-manager-webhook", Found: true, Ready: true, Image: "cm:v1"},
		CertWebhookEndpoints: true,
		OmniCRDPresent:       true,
		OmniCRDEstablished:   true,
		Operator: []DepFact{
			{Name: "local-controller-manager", Found: true, Ready: true, Image: "op:1"},
			{Name: "fleet-controller-manager", Found: true, Ready: true, Image: "op:1"},
		},
		Webhooks: []WebhookFact{{
			Name:  "local-validating-webhook-configuration",
			Found: true,
			Hooks: []HookFact{{CA: true, Service: "local-webhook-service", Namespace: "alloydb-omni-system", Endpoints: true}},
		}},
		PortForwardAllowed: true,
	})
	got := find(rep, "node-resources")
	if got.Status != Fail || !strings.Contains(got.Detail, "arm64") || !strings.Contains(got.Detail, "4.17.0") {
		t.Fatalf("node-resources: %+v", got)
	}
}

type clientOpt func(*[]runtime.Object)

func skipNodes(objs *[]runtime.Object) {
	var kept []runtime.Object
	for _, obj := range *objs {
		if _, ok := obj.(*corev1.Node); ok {
			continue
		}
		kept = append(kept, obj)
	}
	*objs = kept
}

func healthyClients(t *testing.T, extraDyn []runtime.Object, extraTyped []runtime.Object, opts ...clientOpt) *k8s.Clients {
	t.Helper()
	typedObjs := []runtime.Object{
		node("n1"),
		defaultStorageClass(),
		testDeployment("cert-manager", "cert-manager", "cert-manager:v1.14.0"),
		testDeployment("cert-manager", "cert-manager-webhook", "cert-manager-webhook:v1.14.0"),
		testDeployment("alloydb-omni-system", "local-controller-manager", "operator/local:1.2.0"),
		testDeployment("alloydb-omni-system", "fleet-controller-manager", "operator/fleet:1.2.0"),
		endpoints("cert-manager", "cert-manager-webhook"),
		endpoints("alloydb-omni-system", "local-webhook-service"),
		endpoints("alloydb-omni-system", "fleet-webhook-service"),
	}
	typedObjs = append(typedObjs, extraTyped...)
	for _, opt := range opts {
		opt(&typedObjs)
	}
	cs := fake.NewClientset(typedObjs...)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.29.0", Major: "1", Minor: "29"}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		obj.Status.Allowed = true
		return true, obj, nil
	})

	dynObjs := []runtime.Object{
		crd("certificates.cert-manager.io", true),
		crd("issuers.cert-manager.io", true),
		crd("clusterissuers.cert-manager.io", true),
		crd("dbclusters.alloydbomni.dbadmin.goog", true),
		testWebhook("local-validating-webhook-configuration", "ValidatingWebhookConfiguration", "local-webhook-service"),
		testWebhook("local-mutating-webhook-configuration", "MutatingWebhookConfiguration", "local-webhook-service"),
		testWebhook("fleet-validating-webhook-configuration", "ValidatingWebhookConfiguration", "fleet-webhook-service"),
		testWebhook("fleet-mutating-webhook-configuration", "MutatingWebhookConfiguration", "fleet-webhook-service"),
	}
	if !hasDBCluster(extraDyn) {
		dynObjs = append(dynObjs, dbCluster())
	}
	dynObjs = append(dynObjs, extraDyn...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		crdGVR:           "CustomResourceDefinitionList",
		vwhGVR:           "ValidatingWebhookConfigurationList",
		mwhGVR:           "MutatingWebhookConfigurationList",
		k8s.DBClusterGVR: "DBClusterList",
	}, dynObjs...)
	return &k8s.Clients{Dynamic: dyn, Typed: cs, Namespace: "adbo-eugene", Context: "test"}
}

func defaultStorageClass() *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "standard",
			Annotations: map[string]string{
				"storageclass.kubernetes.io/is-default-class": "true",
			},
		},
	}
}

func dropStorageClass(objs *[]runtime.Object) {
	var kept []runtime.Object
	for _, obj := range *objs {
		if _, ok := obj.(*storagev1.StorageClass); ok {
			continue
		}
		kept = append(kept, obj)
	}
	*objs = kept
}

func node(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", KernelVersion: "5.15.0-gke"},
		},
	}
}

func testDeployment(ns, name, image string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: image}}},
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
}

func endpoints(ns, service string) *discoveryv1.EndpointSlice {
	ready := true
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service,
			Namespace: ns,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.8"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	}
}

func TestShortImage(t *testing.T) {
	got := shortImage("gcr.io/alloydb-omni/operator/local-operator@sha256:c0e70410fe6d03385144f17ed7d81f9e353e4238c3df90e4d6523a25ac6ec608")
	if got != "local-operator@sha256:c0e70410fe6d" {
		t.Fatalf("digest: %s", got)
	}
	got = shortImage("quay.io/jetstack/cert-manager-controller:v1.21.2")
	if got != "cert-manager-controller:v1.21.2" {
		t.Fatalf("tag: %s", got)
	}
}

func crd(name string, established bool) *unstructured.Unstructured {
	status := "False"
	if established {
		status = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": name},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Established", "status": status}},
		},
	}}
}

func testWebhook(name, kind, service string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"webhooks": []any{map[string]any{
			"name": "example.alloydbomni.dbadmin.goog",
			"clientConfig": map[string]any{
				"caBundle": "LS0tQQ==",
				"service": map[string]any{
					"name":      service,
					"namespace": "alloydb-omni-system",
				},
			},
		}},
	}}
}

func hasDBCluster(objs []runtime.Object) bool {
	for _, obj := range objs {
		u, ok := obj.(*unstructured.Unstructured)
		if ok && u.GetKind() == "DBCluster" {
			return true
		}
	}
	return false
}

func dbCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "alloydbomni.dbadmin.goog/v1",
		"kind":       "DBCluster",
		"metadata":   map[string]any{"name": "my-db", "namespace": "adbo-eugene"},
		"spec": map[string]any{
			"databaseVersion":           "16.9.0",
			"controlPlaneAgentsVersion": "1.2.0",
		},
		"status": map[string]any{
			"phase": "DBClusterReady",
			"primary": map[string]any{
				"currentDatabaseVersion":           "16.9.0",
				"currentControlPlaneAgentsVersion": "1.2.0",
			},
		},
	}}
}

func find(rep Report, name string) Result {
	for _, result := range rep.Results {
		if result.Name == name {
			return result
		}
	}
	return Result{Name: name, Status: "MISSING", Detail: details(rep)}
}

func assertStatus(t *testing.T, rep Report, name, status string) {
	t.Helper()
	got := find(rep, name)
	if got.Status != status {
		t.Fatalf("%s: got %s (%s), want %s\n%s", name, got.Status, got.Detail, status, details(rep))
	}
}

func details(rep Report) string {
	var b strings.Builder
	for _, result := range rep.Results {
		b.WriteString(result.Name + " " + result.Status + " " + result.Detail + "\n")
	}
	return b.String()
}

func TestRunMissingDefaultStorageClass(t *testing.T) {
	rep, err := Run(context.Background(), healthyClients(t, nil, nil, dropStorageClass), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Failed() {
		t.Fatal("expected a failed installation")
	}
	got := find(rep, "storageclass")
	if got.Status != Fail || !strings.Contains(got.Next, "kubectl get storageclass") {
		t.Fatalf("storageclass: %+v", got)
	}
}

func TestRunNodesForbiddenIsWarn(t *testing.T) {
	c := healthyClients(t, nil, nil)
	c.Typed.(*fake.Clientset).PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("forbidden"))
	})
	rep, err := Run(context.Background(), c, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Fatalf("node 403 should not fail the install:\n%s", details(rep))
	}
	nodes := find(rep, "nodes")
	if nodes.Status != Warn || !strings.Contains(nodes.Detail, "403") {
		t.Fatalf("nodes: %+v", nodes)
	}
}

func TestRunOperatorNamespace(t *testing.T) {
	rep, err := Run(context.Background(), healthyClients(t, nil, nil), false, "other-ns")
	if err != nil {
		t.Fatal(err)
	}
	got := find(rep, "alloydb-operator")
	if got.Status != Fail || !strings.Contains(got.Detail, "was not found") || !strings.Contains(got.Next, "other-ns") || !strings.Contains(got.Next, "grep controller-manager") {
		t.Fatalf("operator: %+v", got)
	}
	ver := find(rep, "versions")
	if ver.Status != Warn || !strings.Contains(ver.Detail, "was not found") || strings.Contains(ver.Detail, "is not ready") {
		t.Fatalf("versions: %+v", ver)
	}
}

func TestRunForbiddenIsWarn(t *testing.T) {
	c := healthyClients(t, nil, nil)
	forbid := func(resource string) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "name", errors.New("no"))
	}
	raw := `customresourcedefinitions.apiextensions.k8s.io "certificates.cert-manager.io" is forbidden: User "system:serviceaccount:default:default" cannot get resource "customresourcedefinitions" in API group "apiextensions.k8s.io" at the cluster scope`
	c.Typed.(*fake.Clientset).PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("deployments")
	})
	c.Typed.(*fake.Clientset).PrependReactor("list", "storageclasses", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("storageclasses")
	})
	c.Typed.(*fake.Clientset).PrependReactor("list", "endpointslices", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("endpointslices")
	})
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "customresourcedefinitions", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Code:    403,
			Reason:  metav1.StatusReasonForbidden,
			Message: raw,
		}}
	})
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "validatingwebhookconfigurations", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("validatingwebhookconfigurations")
	})
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "mutatingwebhookconfigurations", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("mutatingwebhookconfigurations")
	})
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbid("dbclusters")
	})

	rep, err := Run(context.Background(), c, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cert-manager-crds", "cert-manager-webhook", "alloydb-crds", "alloydb-operator", "alloydb-webhooks", "storageclass", "versions"} {
		got := find(rep, name)
		if got.Status != Warn || !strings.Contains(got.Detail, "could not check") || !strings.Contains(got.Detail, "forbidden:") {
			t.Fatalf("%s: %+v", name, got)
		}
		if strings.Contains(got.Detail, "not found") || strings.Contains(got.Detail, "not installed") || strings.Contains(got.Detail, "User ") {
			t.Fatalf("%s leaked a misleading reason: %s", name, got.Detail)
		}
	}
	if find(rep, "rbac").Status != Pass {
		t.Fatalf("rbac should stay a real finding, got %+v", find(rep, "rbac"))
	}
	if rep.Failed() {
		t.Fatalf("403s alone should not fail the install:\n%s", details(rep))
	}
	if !strings.Contains(find(rep, "storageclass").Detail, "list storageclasses") || !strings.Contains(find(rep, "storageclass").Detail, "cluster scope") {
		t.Fatalf("storageclass: %+v", find(rep, "storageclass"))
	}
	if !strings.Contains(find(rep, "cert-manager-crds").Detail, "get customresourcedefinitions (cluster scope)") {
		t.Fatalf("crds: %+v", find(rep, "cert-manager-crds"))
	}
}

func TestCRDNextIsHelm(t *testing.T) {
	omni := omniCRDResult(Snapshot{})
	if !strings.Contains(omni.Next, "helm upgrade -i alloydb-omni oci://${_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${_REPO}/alloydb-omni-argocd-applications") {
		t.Fatalf("omni next: %s", omni.Next)
	}
	cert := certCRDResult(Snapshot{CertCRDs: map[string]bool{}})
	if !strings.Contains(cert.Next, "helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace") {
		t.Fatalf("cert next: %s", cert.Next)
	}
}
