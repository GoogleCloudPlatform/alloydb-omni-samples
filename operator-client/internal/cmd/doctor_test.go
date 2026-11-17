package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

func TestDoctorNotConfigured(t *testing.T) {
	opts := &GlobalOptions{NewClients: func() (*k8s.Clients, error) {
		return &k8s.Clients{}, nil
	}}
	cmd := newDoctorCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err %v", err)
	}
	if strings.Count(out.String(), "installation: not ready") != 1 || strings.Contains(out.String(), "Error:") {
		t.Fatalf("output: %s", out.String())
	}
	if !strings.Contains(out.String(), "kubernetes-api") || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("output: %s", out.String())
	}
	if !strings.Contains(out.String(), "upstream releases were not checked") {
		t.Fatalf("missing note: %s", out.String())
	}
}
