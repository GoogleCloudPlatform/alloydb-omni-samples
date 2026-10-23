package k8s

import "testing"

func TestAdminSecretName(t *testing.T) {
	if got := AdminSecretName("my-db"); got != "db-pw-my-db" {
		t.Fatalf("AdminSecretName = %q", got)
	}
}
