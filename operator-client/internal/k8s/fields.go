package k8s

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// StringField reads a string at path, or "-" when it is unset (e.g. status
// before the operator first reconciles) or obj is nil.
func StringField(obj *unstructured.Unstructured, path ...string) string {
	return FirstStringField(obj, path)
}

// FirstStringField returns the first non-empty string found at paths, in
// order, or "-" when none is set. Use it to prefer a newer field and fall
// back to an older one.
func FirstStringField(obj *unstructured.Unstructured, paths ...[]string) string {
	if obj == nil {
		return "-"
	}
	for _, path := range paths {
		if v, ok, _ := unstructured.NestedString(obj.Object, path...); ok && v != "" {
			return v
		}
	}
	return "-"
}

// ConditionStatus returns status.conditions[type].status ("True", "False",
// "Unknown"), or "-" when the operator has not reported that condition or
// obj is nil.
func ConditionStatus(obj *unstructured.Unstructured, condType string) string {
	if obj == nil {
		return "-"
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != condType {
			continue
		}
		if s, ok := m["status"].(string); ok && s != "" {
			return s
		}
	}
	return "-"
}
