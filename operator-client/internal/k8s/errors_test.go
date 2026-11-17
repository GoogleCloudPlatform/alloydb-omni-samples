package k8s

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFormatNotFoundIncludesWhere(t *testing.T) {
	err := Format(apierrors.NewNotFound(schema.GroupResource{Resource: "dbclusters"}, "my-db"),
		"adbo-eugene", "gke_adbo")
	if err == nil || !strings.Contains(err.Error(), "adbo-eugene") || !strings.Contains(err.Error(), "gke_adbo") {
		t.Fatalf("got %v", err)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal("should still unwrap as NotFound")
	}
}

func TestBriefForbiddenDropsUserIdentity(t *testing.T) {
	err := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure,
		Code:   http.StatusForbidden,
		Reason: metav1.StatusReasonForbidden,
		Message: `customresourcedefinitions.apiextensions.k8s.io "certificates.cert-manager.io" is forbidden: ` +
			`User "system:serviceaccount:default:default" cannot get resource "customresourcedefinitions" ` +
			`in API group "apiextensions.k8s.io" at the cluster scope`,
	}}
	got := Brief(err, "get", "customresourcedefinitions", "")
	want := "forbidden: get customresourcedefinitions (cluster scope)"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestBriefForbiddenUsesHint(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Resource: "storageclasses"}, "", errors.New("no"))
	got := Brief(err, "list", "storageclasses", "")
	if got != "forbidden: list storageclasses (cluster scope)" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatForbidden(t *testing.T) {
	err := Format(apierrors.NewForbidden(schema.GroupResource{Resource: "backups"}, "x", errors.New("no")),
		"ns", "ctx")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("got %v", err)
	}
}

func TestFormatWebhook(t *testing.T) {
	err := Format(errors.New("failed calling webhook: connection refused"), "", "")
	if err == nil || !strings.Contains(err.Error(), "cert-manager") {
		t.Fatalf("got %v", err)
	}
}

func TestFormatPassthrough(t *testing.T) {
	orig := errors.New("cluster is stopped")
	if Format(orig, "ns", "ctx") != orig {
		t.Fatal("non-API errors must pass through")
	}
	if Format(nil, "", "") != nil {
		t.Fatal("nil stays nil")
	}
}

func TestFormatIdempotent(t *testing.T) {
	first := Format(apierrors.NewNotFound(schema.GroupResource{Resource: "backups"}, "x"), "ns", "ctx")
	second := Format(first, "", "")
	if second != first {
		t.Fatalf("second Format changed the error:\n%s\n%s", first, second)
	}
	if strings.Count(second.Error(), "check the name") != 1 {
		t.Fatalf("doubled message: %v", second)
	}
}

func TestFormatErrUsesClientLocation(t *testing.T) {
	c := &Clients{Namespace: "adbo-eugene", Context: "gke_adbo"}
	err := c.FormatErr(apierrors.NewForbidden(schema.GroupResource{Resource: "backups"}, "x", errors.New("no")))
	if err == nil || !strings.Contains(err.Error(), "adbo-eugene") || !strings.Contains(err.Error(), "gke_adbo") {
		t.Fatalf("got %v", err)
	}
}

func TestFormatCRDsMissing(t *testing.T) {
	se := apierrors.NewGenericServerResponse(http.StatusNotFound, "get",
		schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbcluster"},
		"", "404 not found", 0, true)

	err := Format(se, "ns", "ctx")
	if err == nil || !strings.Contains(err.Error(), "CRDs") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "check the name") {
		t.Fatalf("CRD 404 should not look like a missing object: %v", err)
	}
}

func TestFormatUnauthorized(t *testing.T) {
	err := Format(apierrors.NewUnauthorized("token expired"), "ns", "ctx")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "RBAC") {
		t.Fatal("401 should not mention RBAC")
	}
}
