package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version"
)

func fakeOperatorDeployment(ns, name string, labels map[string]string, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "manager", Image: image},
				{Name: "kube-rbac-proxy", Image: "gcr.io/alloydb-omni/kubebuilder/kube-rbac-proxy:v0.16.0"},
			},
		}}},
	}
}

func fakeTypedOpts(objs ...runtime.Object) *GlobalOptions {
	typed := kubefake.NewClientset(objs...)
	return &GlobalOptions{
		Output:  output.OutputTable,
		Timeout: time.Minute,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Typed: typed, Namespace: "default"}, nil
		},
	}
}

func runVersionCmd(t *testing.T, opts *GlobalOptions, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := newVersionCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("adbo version %v: %v\nstderr: %s", args, err, errOut.String())
	}
	return out.String(), errOut.String()
}

func setClientVersion(t *testing.T, v string) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = v
}

func TestVersionShowsOperatorFromDefaultInstall(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := fakeTypedOpts(fakeOperatorDeployment("alloydb-omni-system", "fleet-controller-manager",
		map[string]string{"fleet-control-plane": "controller-manager"},
		"gcr.io/alloydb-omni/operator/fleet-operator:1.7.0"))

	out, _ := runVersionCmd(t, opts)

	for _, want := range []string{"v0.1.0", "1.7.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "v0.16.0") {
		t.Fatalf("reported the kube-rbac-proxy sidecar tag instead of the operator:\n%s", out)
	}
}

func TestVersionFindsOperatorInCustomNamespaceByLabel(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := fakeTypedOpts(fakeOperatorDeployment("dbs-operator", "my-release-fleet",
		map[string]string{"fleet-control-plane": "controller-manager"},
		"registry.local:5000/omni/operator/fleet-operator:1.6.1"))

	out, _ := runVersionCmd(t, opts)

	if !strings.Contains(out, "1.6.1") {
		t.Fatalf("operator in a non-default namespace not found:\n%s", out)
	}
}

func TestVersionReportsOperatorNotInstalled(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := fakeTypedOpts()

	out, _ := runVersionCmd(t, opts)

	if !strings.Contains(out, "not installed") {
		t.Fatalf("want 'not installed' when no operator Deployment exists:\n%s", out)
	}
}

func TestVersionClientOnlySkipsCluster(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := &GlobalOptions{
		Output:  output.OutputTable,
		Timeout: time.Minute,
		NewClients: func() (*k8s.Clients, error) {
			t.Fatal("--client must not contact the cluster")
			return nil, nil
		},
	}

	out, _ := runVersionCmd(t, opts, "--client")

	if !strings.Contains(out, "v0.1.0") {
		t.Fatalf("client version missing:\n%s", out)
	}
	if strings.Contains(out, "operator") {
		t.Fatalf("--client should not print an operator row:\n%s", out)
	}
}

func TestVersionWithoutKubeconfigStillSucceeds(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := &GlobalOptions{
		Output:     output.OutputTable,
		Timeout:    time.Minute,
		NewClients: func() (*k8s.Clients, error) { return nil, errors.New("no kubeconfig") },
	}

	out, errOut := runVersionCmd(t, opts)

	if !strings.Contains(out, "v0.1.0") {
		t.Fatalf("client version missing:\n%s", out)
	}
	if !strings.Contains(errOut, "no kubeconfig") {
		t.Fatalf("want a warning explaining why the operator version is unknown, stderr:\n%s", errOut)
	}
}

func TestVersionJSON(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	opts := fakeTypedOpts(fakeOperatorDeployment("alloydb-omni-system", "fleet-controller-manager",
		map[string]string{"fleet-control-plane": "controller-manager"},
		"gcr.io/alloydb-omni/operator/fleet-operator:1.7.0"))
	opts.Output = output.OutputJSON

	out, _ := runVersionCmd(t, opts)

	var got struct {
		Client struct {
			Version string `json:"version"`
		} `json:"client"`
		Operator struct {
			Version   string `json:"version"`
			Namespace string `json:"namespace"`
		} `json:"operator"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if got.Client.Version != "v0.1.0" || got.Operator.Version != "1.7.0" || got.Operator.Namespace != "alloydb-omni-system" {
		t.Fatalf("json = %+v", got)
	}
}

func TestImageTag(t *testing.T) {
	for _, tc := range []struct{ image, want string }{
		{"gcr.io/alloydb-omni/operator/fleet-operator:1.7.0", "1.7.0"},
		{"registry.local:5000/omni/operator/fleet-operator:1.6.1", "1.6.1"},
		{"registry.local:5000/omni/operator/fleet-operator", "latest"},
		{"gcr.io/x/fleet-operator@sha256:0123456789abcdef0123", "sha256:0123456789ab"},
		{"gcr.io/x/fleet-operator:1.7.0@sha256:0123456789abcdef0123", "1.7.0"},
	} {
		if got := imageTag(tc.image); got != tc.want {
			t.Errorf("imageTag(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

// namespaceScopedOpts is a user who may read Deployments only in ns:
// the default install namespace and cluster-wide lists are Forbidden.
func namespaceScopedOpts(ns string, objs ...runtime.Object) *GlobalOptions {
	typed := kubefake.NewClientset(objs...)
	forbidden := func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == ns {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("namespace-scoped user"))
	}
	typed.PrependReactor("get", "deployments", forbidden)
	typed.PrependReactor("list", "deployments", forbidden)
	return &GlobalOptions{
		Output:  output.OutputTable,
		Timeout: time.Minute,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Typed: typed, Namespace: ns, Context: "dev-gke"}, nil
		},
	}
}

func TestVersionFindsOperatorInOwnNamespaceWhenListingAllIsForbidden(t *testing.T) {
	opts := namespaceScopedOpts("team-a", fakeOperatorDeployment("team-a", "fleet",
		map[string]string{"fleet-control-plane": "controller-manager"}, "gcr.io/alloydb-omni/operator/fleet-operator:1.6.1"))

	info, err := findOperator(context.Background(), opts)
	if err != nil || info.Version != "1.6.1" {
		t.Fatalf("want the operator in the user's namespace, got %+v, %v", info, err)
	}
}

func TestVersionForbiddenEverywhereIsAnErrorNotNotInstalled(t *testing.T) {
	opts := namespaceScopedOpts("team-a")

	info, err := findOperator(context.Background(), opts)
	if err == nil {
		t.Fatalf("an operator adbo isn't allowed to look for must not be reported as not installed: %+v", info)
	}
	for _, want := range []string{"permission denied", `"team-a"`, `"dev-gke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestVersionPicksOperatorDeterministically(t *testing.T) {
	label := map[string]string{"fleet-control-plane": "controller-manager"}
	zeta := fakeOperatorDeployment("zeta", "fleet", label, "gcr.io/x/fleet-operator:1.7.0")
	alpha := fakeOperatorDeployment("alpha", "fleet", label, "gcr.io/x/fleet-operator:1.6.1")
	teamA := fakeOperatorDeployment("team-a", "fleet", label, "gcr.io/x/fleet-operator:1.5.0")
	for _, tc := range []struct{ ns, want string }{
		{"team-a", "1.5.0"},  // the current namespace wins
		{"default", "1.6.1"}, // otherwise the first namespace by name
	} {
		typed := kubefake.NewClientset()
		// The API server's order isn't something to rely on; return zeta first.
		typed.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, &appsv1.DeploymentList{Items: []appsv1.Deployment{*zeta, *alpha, *teamA}}, nil
		})
		opts := &GlobalOptions{Timeout: time.Minute, NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Typed: typed, Namespace: tc.ns}, nil
		}}
		info, err := findOperator(context.Background(), opts)
		if err != nil || info.Version != tc.want {
			t.Errorf("namespace %s: got %+v, %v; want version %s", tc.ns, info, err, tc.want)
		}
	}
}

func TestOperatorImageMatchedByName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []string
		want   string
	}{
		{"no registry, e.g. loaded into kind", []string{"fleet-operator:1.7.0"}, "1.7.0"},
		{"similarly named sidecar first", []string{"gcr.io/x/fleet-operator-metrics:9.9.9", "gcr.io/alloydb-omni/operator/fleet-operator:1.7.0"}, "1.7.0"},
		{"registry with a port", []string{"localhost:5000/fleet-operator:1.6.1"}, "1.6.1"},
	} {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment, Namespace: operatorNamespace}}
		for _, img := range tc.images {
			d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, corev1.Container{Name: "c", Image: img})
		}
		if got := operatorFrom(d).Version; got != tc.want {
			t.Errorf("%s: version = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRootRegistersVersion(t *testing.T) {
	setClientVersion(t, "v0.1.0")
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version", "--client"})

	if err := root.Execute(); err != nil {
		t.Fatalf("adbo version --client: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "v0.1.0") {
		t.Fatalf("adbo version --client output:\n%s", out.String())
	}
}

func operatorJSON(t *testing.T, opts *GlobalOptions, args ...string) map[string]any {
	t.Helper()
	opts.Output = output.OutputJSON
	out, _ := runVersionCmd(t, opts, args...)
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	op, _ := got["operator"].(map[string]any)
	return op
}

func TestVersionJSONTellsNotInstalledFromUnknown(t *testing.T) {
	setClientVersion(t, "v0.1.0")

	notInstalled := operatorJSON(t, fakeTypedOpts())
	if notInstalled == nil || notInstalled["installed"] != false || notInstalled["version"] != nil {
		t.Fatalf("no operator: want {installed: false} with no version, got %v", notInstalled)
	}

	installed := operatorJSON(t, fakeTypedOpts(fakeOperatorDeployment("alloydb-omni-system", "fleet-controller-manager",
		map[string]string{"fleet-control-plane": "controller-manager"}, "gcr.io/alloydb-omni/operator/fleet-operator:1.7.0")))
	if installed["installed"] != true || installed["version"] != "1.7.0" {
		t.Fatalf("installed operator: got %v", installed)
	}

	unreachable := operatorJSON(t, &GlobalOptions{
		Timeout:    time.Minute,
		NewClients: func() (*k8s.Clients, error) { return nil, errors.New("no kubeconfig") },
	})
	if unreachable == nil || unreachable["error"] == nil || unreachable["installed"] != nil {
		t.Fatalf("lookup failure: want {error: ...} and no installed claim, got %v", unreachable)
	}

	if clientOnly := operatorJSON(t, fakeTypedOpts(), "--client"); clientOnly != nil {
		t.Fatalf("--client: want no operator key, got %v", clientOnly)
	}
}
