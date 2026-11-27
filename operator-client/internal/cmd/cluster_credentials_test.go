package cmd

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func clusterWithEndpoint(value string) *unstructured.Unstructured {
	obj := dbcluster("my-db", false, "Ready")
	obj.Object["status"].(map[string]any)["primary"] = map[string]any{
		"endpoints": []any{map[string]any{"name": "primary", "value": value}},
	}
	return obj
}

func credentialClients(obj *unstructured.Unstructured, password string) *k8s.Clients {
	c := fakeClients(obj)
	c.Typed = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-my-db", Namespace: "adbo-eugene"},
		Data:       map[string][]byte{"my-db": []byte(password)},
	})
	return c
}

func TestClusterCredentialsMasksPassword(t *testing.T) {
	const secret = "p@ss word"
	opts := &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return credentialClients(clusterWithEndpoint("10.0.0.8:5433"), secret), nil
	}}
	cmd := newClusterCredentialsCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"my-db"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, secret) || !strings.Contains(s, "****") || !strings.Contains(s, "10.0.0.8") || !strings.Contains(s, "5433") {
		t.Fatalf("output: %s", s)
	}
	if !strings.Contains(s, "user=postgres") || !strings.Contains(s, "dbname=postgres") || !strings.Contains(s, "postgres://postgres:") {
		t.Fatalf("output: %s", s)
	}
}

func TestClusterCredentialsReveal(t *testing.T) {
	const secret = "p@ss word"
	opts := &GlobalOptions{
		Yes:    true,
		Output: output.OutputJSON,
		NewClients: func() (*k8s.Clients, error) {
			return credentialClients(clusterWithEndpoint("10.0.0.8:5433"), secret), nil
		},
	}
	cmd := newClusterCredentialsCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"my-db", "--reveal-password"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, secret) || !strings.Contains(s, "p%40ss%20word") {
		t.Fatalf("output: %s", s)
	}
}

func TestClusterCredentialsRevealDeclined(t *testing.T) {
	const secret = "p@ss word"
	opts := &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return credentialClients(clusterWithEndpoint("10.0.0.8:5433"), secret), nil
	}}
	cmd := newClusterCredentialsCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{"my-db", "--reveal-password"})
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
		t.Fatalf("err %v output %s", err, out.String())
	}
}

func TestDSNQuotesPassword(t *testing.T) {
	got := dsn("10.0.0.8", 5432, "postgres", "postgres", `p a\ss'word`)
	if got != `host=10.0.0.8 port=5432 user=postgres dbname=postgres password='p a\\ss\'word'` {
		t.Fatalf("dsn: %s", got)
	}
}

func TestHostPortStripsIPv6Brackets(t *testing.T) {
	host, port, ok := hostPort("[::1]", 5432)
	if !ok || host != "::1" || port != 5432 {
		t.Fatalf("host %q port %d ok %v", host, port, ok)
	}
	joined := net.JoinHostPort(host, strconv.Itoa(int(port)))
	if joined != "[::1]:5432" {
		t.Fatalf("joined %s", joined)
	}
}

func TestClusterCredentialsMissingEndpoint(t *testing.T) {
	opts := &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return credentialClients(dbcluster("my-db", false, "Ready"), "secret"), nil
	}}
	cmd := newClusterCredentialsCommand(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-db"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no primary endpoint") {
		t.Fatalf("err %v", err)
	}
}

func TestPrimaryHostPortFallsBack(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"primary": map[string]any{"url": "postgres://10.1.2.3:5432/postgres"}},
	}}
	host, port := primaryHostPort(obj)
	if host != "10.1.2.3" || port != 5432 {
		t.Fatalf("url: %s %d", host, port)
	}
	obj.Object["status"].(map[string]any)["primary"] = map[string]any{"endpoint": "db.example"}
	host, port = primaryHostPort(obj)
	if host != "db.example" || port != 5432 {
		t.Fatalf("endpoint: %s %d", host, port)
	}
}
