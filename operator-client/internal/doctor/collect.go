package doctor

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8srest "k8s.io/client-go/rest"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

var (
	crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	vwhGVR = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mwhGVR = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}

	omniWebhookNames = []string{
		"local-validating-webhook-configuration",
		"local-mutating-webhook-configuration",
		"fleet-validating-webhook-configuration",
		"fleet-mutating-webhook-configuration",
	}
	operatorDeployments = []string{"local-controller-manager", "fleet-controller-manager"}
)

// Run reads the cluster and evaluates it. API failures become Fail rows, not a bare error.
func Run(ctx context.Context, c *k8s.Clients, allNamespaces bool, operatorNamespace string) (Report, error) {
	// API-server warnings (deprecated Endpoints, and similar) are not part of the report.
	k8srest.SetDefaultWarningHandler(k8srest.NoWarnings{})
	if c == nil || c.Typed == nil || c.Dynamic == nil {
		return Evaluate(Snapshot{APIError: "kubernetes client is not configured"}), nil
	}
	snap, err := collect(ctx, c, allNamespaces, operatorNamespace)
	if err != nil {
		return Report{}, err
	}
	return Evaluate(snap), nil
}

func collect(ctx context.Context, c *k8s.Clients, allNamespaces bool, operatorNamespace string) (Snapshot, error) {
	if operatorNamespace == "" {
		operatorNamespace = cluster.OperatorNamespace
	}
	snap := Snapshot{
		Context:           c.Context,
		Namespace:         c.Namespace,
		AllNamespaces:     allNamespaces,
		OperatorNamespace: operatorNamespace,
		CertCRDs:          map[string]bool{},
	}
	info, err := c.Typed.Discovery().ServerVersion()
	if err != nil {
		snap.APIError = k8s.Brief(err, "get", "version", "")
		return snap, nil
	}
	snap.ServerGit = info.GitVersion

	collectRBAC(ctx, c, &snap)
	collectCertManager(ctx, c, &snap)
	collectOmni(ctx, c, &snap)
	collectStorageClasses(ctx, c, &snap)
	collectNodes(ctx, c, &snap)
	collectClusters(ctx, c, &snap)
	return snap, nil
}

type accessCheck struct {
	verb, group, resource, subresource, namespace string
	required                                      bool
}

func collectRBAC(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	ns := c.Namespace
	checks := []accessCheck{
		{verb: "get", group: "alloydbomni.dbadmin.goog", resource: "dbclusters", namespace: ns, required: true},
		{verb: "list", group: "alloydbomni.dbadmin.goog", resource: "dbclusters", namespace: ns, required: true},
		{verb: "get", resource: "pods", namespace: ns, required: true},
		{verb: "list", resource: "pods", namespace: ns, required: true},
		{verb: "get", resource: "secrets", namespace: ns, required: true},
		{verb: "list", resource: "secrets", namespace: ns, required: true},
		{verb: "get", resource: "events", namespace: ns, required: true},
		{verb: "list", resource: "events", namespace: ns, required: true},
		{verb: "get", resource: "nodes", required: true},
		{verb: "get", group: "apiextensions.k8s.io", resource: "customresourcedefinitions", required: true},
		{verb: "get", group: "admissionregistration.k8s.io", resource: "validatingwebhookconfigurations", required: true},
		{verb: "create", resource: "pods", subresource: "portforward", namespace: ns, required: false},
	}
	for _, check := range checks {
		allowed, err := allowed(ctx, c, check)
		if err != nil {
			snap.RBACError = k8s.Brief(err, "create", "selfsubjectaccessreviews", "")
			return
		}
		label := check.verb + " " + check.resource
		if check.subresource != "" {
			label += "/" + check.subresource
		}
		if !allowed && check.required {
			snap.RBACDenied = append(snap.RBACDenied, label)
		}
		if check.subresource == "portforward" {
			snap.PortForwardAllowed = allowed
		}
	}
}

func allowed(ctx context.Context, c *k8s.Clients, check accessCheck) (bool, error) {
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace:   check.namespace,
				Verb:        check.verb,
				Group:       check.group,
				Resource:    check.resource,
				Subresource: check.subresource,
			},
		},
	}
	resp, err := c.Typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("SelfSubjectAccessReview %s %s: %w", check.verb, check.resource, err)
	}
	return resp.Status.Allowed, nil
}

func collectCertManager(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	for _, name := range certManagerCRDs {
		if snap.CertCRDForbidden != "" || snap.CertCRDError != "" {
			break
		}
		obj, err := c.Cluster(crdGVR).Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			snap.CertCRDs[name] = false
		case apierrors.IsForbidden(err):
			snap.CertCRDForbidden = couldNotCheck(err, "get", "customresourcedefinitions", "")
		case err != nil:
			snap.CertCRDError = k8s.Brief(err, "get", "customresourcedefinitions", "")
		default:
			snap.CertCRDs[name] = obj != nil
		}
	}
	snap.CertController = deployment(ctx, c, cluster.CertManagerNamespace, "cert-manager")
	snap.CertWebhook = deployment(ctx, c, cluster.CertManagerNamespace, "cert-manager-webhook")
	ready, err := endpointsReady(ctx, c, cluster.CertManagerNamespace, "cert-manager-webhook")
	switch {
	case apierrors.IsForbidden(err):
		snap.CertWebhookEndpointsForbidden = couldNotCheck(err, "list", "endpointslices", cluster.CertManagerNamespace)
	case err != nil && !apierrors.IsNotFound(err):
		snap.CertWebhookEndpointsError = k8s.Brief(err, "list", "endpointslices", cluster.CertManagerNamespace)
	default:
		snap.CertWebhookEndpoints = ready
	}
	snap.CertImagePullPod, snap.CertImagePullReason = imagePull(ctx, c, cluster.CertManagerNamespace)
}

func collectOmni(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	obj, err := c.Cluster(crdGVR).Get(ctx, "dbclusters.alloydbomni.dbadmin.goog", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		snap.OmniCRDPresent = false
	case apierrors.IsForbidden(err):
		snap.OmniCRDForbidden = couldNotCheck(err, "get", "customresourcedefinitions", "")
	case err != nil:
		snap.OmniCRDError = k8s.Brief(err, "get", "customresourcedefinitions", "")
	default:
		snap.OmniCRDPresent = true
		snap.OmniCRDEstablished = established(obj)
	}
	for _, name := range operatorDeployments {
		snap.Operator = append(snap.Operator, deployment(ctx, c, snap.OperatorNamespace, name))
	}
	snap.OperatorImagePullPod, snap.OperatorImagePullReason = imagePull(ctx, c, snap.OperatorNamespace)
	for _, name := range omniWebhookNames {
		gvr := vwhGVR
		if strings.Contains(name, "mutating") {
			gvr = mwhGVR
		}
		snap.Webhooks = append(snap.Webhooks, webhook(ctx, c, gvr, name, snap.OperatorNamespace))
	}
}

func collectStorageClasses(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	list, err := c.Typed.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsForbidden(err):
		snap.StorageClassForbidden = couldNotCheck(err, "list", "storageclasses", "")
		return
	case err != nil:
		snap.StorageClassError = k8s.Brief(err, "list", "storageclasses", "")
		return
	}
	for i := range list.Items {
		if list.Items[i].Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			snap.DefaultStorageClass = list.Items[i].Name
			return
		}
	}
}

func collectNodes(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	list, err := c.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if apierrors.IsForbidden(err) {
		snap.NodesForbidden = true
		return
	}
	if err != nil {
		snap.NodesError = k8s.Brief(err, "list", "nodes", "")
		return
	}
	for i := range list.Items {
		n := &list.Items[i]
		snap.Nodes = append(snap.Nodes, NodeFact{
			Name:   n.Name,
			Arch:   n.Status.NodeInfo.Architecture,
			Kernel: n.Status.NodeInfo.KernelVersion,
			CPU:    n.Status.Allocatable[corev1.ResourceCPU],
			Memory: n.Status.Allocatable[corev1.ResourceMemory],
		})
	}
}

func collectClusters(ctx context.Context, c *k8s.Clients, snap *Snapshot) {
	ns := c.Namespace
	if snap.AllNamespaces {
		ns = ""
	}
	list, err := c.InNamespace(k8s.DBClusterGVR, ns).List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsForbidden(err):
		snap.ClustersForbidden = couldNotCheck(err, "list", "dbclusters", ns)
		return
	case err != nil:
		snap.ClustersError = k8s.Brief(err, "list", "dbclusters", ns)
		return
	}
	for i := range list.Items {
		obj := &list.Items[i]
		snap.Clusters = append(snap.Clusters, ClusterFact{
			Namespace:     obj.GetNamespace(),
			Name:          obj.GetName(),
			SpecDB:        cluster.DatabaseVersion(obj),
			RunningDB:     cluster.RunningDatabaseVersion(obj),
			SpecAgents:    cluster.AgentsVersion(obj),
			RunningAgents: cluster.RunningAgentsVersion(obj),
		})
	}
}

func deployment(ctx context.Context, c *k8s.Clients, ns, name string) DepFact {
	dep, err := c.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return DepFact{Name: name}
	case apierrors.IsForbidden(err):
		return DepFact{Name: name, Forbidden: couldNotCheck(err, "get", "deployments", ns)}
	case err != nil:
		return DepFact{Name: name, Error: k8s.Brief(err, "get", "deployments", ns)}
	default:
		return DepFact{Name: name, Found: true, Ready: deploymentReady(dep), Image: containerImages(dep)}
	}
}

func deploymentReady(dep *appsv1.Deployment) bool {
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	return want > 0 && dep.Status.ReadyReplicas >= want && dep.Status.AvailableReplicas >= want
}

func containerImages(dep *appsv1.Deployment) string {
	var parts []string
	for _, container := range dep.Spec.Template.Spec.Containers {
		if container.Image != "" {
			parts = append(parts, container.Image)
		}
	}
	return joinComma(parts)
}

func endpointsReady(ctx context.Context, c *k8s.Clients, ns, name string) (bool, error) {
	// EndpointSlice replaced Endpoints. The v1 Endpoints API warns on every call
	// on Kubernetes 1.33+, which buried the doctor table.
	list, err := c.Typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + name,
	})
	if err != nil {
		return false, err
	}
	for i := range list.Items {
		for _, ep := range list.Items[i].Endpoints {
			if len(ep.Addresses) == 0 {
				continue
			}
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}

func imagePull(ctx context.Context, c *k8s.Clients, ns string) (podName, reason string) {
	list, err := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", ""
	}
	for i := range list.Items {
		pod := &list.Items[i]
		statuses := append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...)
		statuses = append(statuses, pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
			if cs.State.Waiting == nil {
				continue
			}
			switch cs.State.Waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				return pod.Name, cs.State.Waiting.Reason
			}
		}
	}
	return "", ""
}

func webhook(ctx context.Context, c *k8s.Clients, gvr schema.GroupVersionResource, name, operatorNamespace string) WebhookFact {
	obj, err := c.Cluster(gvr).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return WebhookFact{Name: name}
	case apierrors.IsForbidden(err):
		return WebhookFact{Name: name, Forbidden: couldNotCheck(err, "get", gvr.Resource, "")}
	case err != nil:
		return WebhookFact{Name: name, Error: k8s.Brief(err, "get", gvr.Resource, "")}
	}
	fact := WebhookFact{Name: name, Found: true}
	hooks, _, _ := unstructured.NestedSlice(obj.Object, "webhooks")
	for _, raw := range hooks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		h := HookFact{
			CA:        strings.TrimSpace(mapString(m, "clientConfig", "caBundle")) != "",
			Service:   mapString(m, "clientConfig", "service", "name"),
			Namespace: mapString(m, "clientConfig", "service", "namespace"),
		}
		if h.Service != "" {
			ns := h.Namespace
			if ns == "" {
				ns = operatorNamespace
			}
			ready, err := endpointsReady(ctx, c, ns, h.Service)
			switch {
			case apierrors.IsForbidden(err):
				h.EndpointsForbidden = couldNotCheck(err, "list", "endpointslices", ns)
			case err != nil && !apierrors.IsNotFound(err):
				h.EndpointsError = k8s.Brief(err, "list", "endpointslices", ns)
			default:
				h.Endpoints = ready
			}
		}
		fact.Hooks = append(fact.Hooks, h)
	}
	return fact
}

func established(obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Established" && m["status"] == "True" {
			return true
		}
	}
	return false
}

func mapString(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

func couldNotCheck(err error, verb, resource, namespace string) string {
	return "could not check: " + k8s.Brief(err, verb, resource, namespace)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
