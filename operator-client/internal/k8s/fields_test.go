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

func TestFirstStringField(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"new": "", "old": "10.0.0.1"},
	}}
	if got := FirstStringField(obj, []string{"status", "new"}, []string{"status", "old"}); got != "10.0.0.1" {
		t.Errorf("empty first path should fall back, got %q", got)
	}
	if got := FirstStringField(obj, []string{"status", "missing"}); got != "-" {
		t.Errorf("missing = %q, want -", got)
	}
}

func TestFieldsNilObject(t *testing.T) {
	if StringField(nil, "status") != "-" || ConditionStatus(nil, "Ready") != "-" {
		t.Error("nil object should render as -")
	}
}
