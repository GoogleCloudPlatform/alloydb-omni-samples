package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStringField(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"phase": "Ready", "empty": ""},
	}}
	for path, want := range map[string]string{"phase": "Ready", "empty": "-", "missing": "-"} {
		if got := StringField(obj, "status", path); got != want {
			t.Errorf("StringField(status.%s) = %q, want %q", path, got, want)
		}
	}
}

func TestConditionStatus(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False"},
			map[string]any{"type": "HAReady", "status": "True"},
		}},
	}}
	if got := ConditionStatus(obj, "HAReady"); got != "True" {
		t.Errorf("HAReady = %q, want True", got)
	}
	if got := ConditionStatus(obj, "Missing"); got != "-" {
		t.Errorf("Missing = %q, want -", got)
	}
}
