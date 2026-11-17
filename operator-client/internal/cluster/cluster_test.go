package cluster

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSelectPrimary(t *testing.T) {
	one := []PodInfo{{Name: "db-0"}}
	got, err := SelectPrimary(one, nil, "")
	if err != nil || got != "db-0" {
		t.Fatalf("single pod: %q %v", got, err)
	}

	if _, err := SelectPrimary(nil, nil, ""); err == nil || !strings.Contains(err.Error(), "no database pod") {
		t.Fatalf("empty: %v", err)
	}

	many := []PodInfo{{Name: "a"}, {Name: "b"}}
	if _, err := SelectPrimary(many, nil, ""); err == nil || !strings.Contains(err.Error(), "--pod") {
		t.Fatalf("ambiguous: %v", err)
	}

	got, err = SelectPrimary(many, nil, "b")
	if err != nil || got != "b" {
		t.Fatalf("explicit: %q %v", got, err)
	}

	labeled := []PodInfo{
		{Name: "standby", Labels: map[string]string{LabelHARole: "Standby"}},
		{Name: "primary", Labels: map[string]string{LabelHARole: "Primary"}},
	}
	got, err = SelectPrimary(labeled, nil, "")
	if err != nil || got != "primary" {
		t.Fatalf("pod role: %q %v", got, err)
	}

	fromInstance := []PodInfo{{Name: "inst-1-0"}, {Name: "inst-2-0"}}
	instances := []InstanceInfo{{Name: "inst-2", Role: "Primary"}, {Name: "inst-1", Role: "Standby"}}
	got, err = SelectPrimary(fromInstance, instances, "")
	if err != nil || got != "inst-2-0" {
		t.Fatalf("instance role: %q %v", got, err)
	}
}

func TestSelectReadOnly(t *testing.T) {
	primary := PodInfo{Name: "primary-0", Labels: map[string]string{LabelHARole: RolePrimary}}
	standby := PodInfo{Name: "standby-0", Labels: map[string]string{LabelHARole: RoleStandby}}
	pool := PodInfo{Name: "my-db-rp-0", Labels: map[string]string{
		"alloydbomni.internal.dbadmin.goog/instance": "my-db-rp",
	}}
	otherPool := PodInfo{Name: "my-db-rp-1", Labels: map[string]string{
		"alloydbomni.internal.dbadmin.goog/instance": "my-db-rp-b",
	}}

	got, err := SelectReadOnly([]PodInfo{primary, standby, pool}, []string{"my-db-rp"})
	if err != nil || got != "my-db-rp-0" {
		t.Fatalf("read pool: %q %v", got, err)
	}

	got, err = SelectReadOnly([]PodInfo{primary, standby}, nil)
	if err != nil || got != "standby-0" {
		t.Fatalf("standby: %q %v", got, err)
	}

	if _, err := SelectReadOnly([]PodInfo{primary}, nil); err == nil || !strings.Contains(err.Error(), "no read-only pod") {
		t.Fatalf("none: %v", err)
	}

	if _, err := SelectReadOnly([]PodInfo{pool, otherPool}, []string{"my-db-rp", "my-db-rp-b"}); err == nil || !strings.Contains(err.Error(), "--pod") {
		t.Fatalf("ambiguous pool: %v", err)
	}

	second := PodInfo{Name: "standby-1", Labels: map[string]string{LabelHARole: RoleStandby}}
	if _, err := SelectReadOnly([]PodInfo{standby, second}, nil); err == nil || !strings.Contains(err.Error(), "standby") {
		t.Fatalf("ambiguous standby: %v", err)
	}
}

func TestDBPortAndSecretName(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"primarySpec": map[string]any{
				"port": float64(6789),
				"adminUser": map[string]any{
					"passwordRef": map[string]any{"name": "custom-pw"},
				},
			},
		},
	}}
	if got := DBPort(obj); got != 6789 {
		t.Fatalf("port %d", got)
	}
	if got := PasswordSecretName("my-db", obj); got != "custom-pw" {
		t.Fatalf("secret %s", got)
	}
	if got := PasswordSecretName("my-db", nil); got != "db-pw-my-db" {
		t.Fatalf("default secret %s", got)
	}
	if got := DBPort(nil); got != DefaultDBPort {
		t.Fatalf("default port %d", got)
	}
}

func TestSecretBytesOmitsValue(t *testing.T) {
	sec := &corev1.Secret{Data: map[string][]byte{"my-db": []byte("s3cret-password")}}
	sec.Name = "db-pw-my-db"
	got, err := SecretBytes(sec, "my-db")
	if err != nil || string(got) != "s3cret-password" {
		t.Fatalf("bytes: %q %v", got, err)
	}
	_, err = SecretBytes(sec, "missing")
	if err == nil || strings.Contains(err.Error(), "s3cret-password") {
		t.Fatalf("error leaked or missing: %v", err)
	}
}

func TestRunningVersionFallback(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"databaseVersion": "16.9.0"},
		"status": map[string]any{
			"primary": map[string]any{"currentDatabaseVersion": "16.9.0"},
			"criticalIncidents": []any{
				map[string]any{"code": "DBSE1012", "message": "disk full"},
			},
		},
	}}
	if RunningDatabaseVersion(obj) != "16.9.0" {
		t.Fatalf("running %s", RunningDatabaseVersion(obj))
	}
	lines := CriticalIncidents(obj)
	if len(lines) != 1 || !strings.Contains(lines[0], "DBSE1012") {
		t.Fatalf("incidents %#v", lines)
	}
}

func TestBelongs(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"kind":     "Backup",
		"metadata": map[string]any{"name": "b1"},
		"spec":     map[string]any{"dbclusterRef": "my-db"},
	}}
	if !Belongs(obj, "my-db") || Belongs(obj, "other") {
		t.Fatal("dbclusterRef match failed")
	}
	if !Belongs(obj, "") {
		t.Fatal("empty cluster should match all")
	}
}
