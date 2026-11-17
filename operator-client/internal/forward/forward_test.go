package forward

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"
)

func TestForwardRejectsMissingConfig(t *testing.T) {
	_, _, err := (Tunnel{}).Forward(context.Background(), "ns", "pod", 0, 5432)
	if err == nil {
		t.Fatal("expected missing REST config to fail")
	}
}

func TestForwardRejectsBadPort(t *testing.T) {
	_, _, err := (Tunnel{REST: &rest.Config{Host: "https://example.com"}}).Forward(context.Background(), "ns", "pod", 0, 0)
	if err == nil {
		t.Fatal("expected remote port 0 to fail")
	}
}
