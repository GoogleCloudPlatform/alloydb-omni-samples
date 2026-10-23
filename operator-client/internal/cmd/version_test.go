package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

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
