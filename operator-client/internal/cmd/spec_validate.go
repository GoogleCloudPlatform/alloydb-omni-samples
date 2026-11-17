package cmd

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strconv"
)

func validateSpec(old, updated *unstructured.Unstructured) error {
	if old == nil {
		old = &unstructured.Unstructured{Object: map[string]any{}}
	}
	const maxStandbys = 5
	if v, found, _ := unstructured.NestedFieldNoCopy(updated.Object, standbysPath...); found {
		n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		if err != nil || n < 0 || n > maxStandbys {
			return fmt.Errorf("spec.availability.numberOfStandbys must be between 0 and %d, got %v", maxStandbys, v)
		}
	}

	for _, field := range []string{"cpu", "memory"} {
		path := append(append([]string{}, resourcesPath...), field)
		want := quantityField(updated, path...)
		if want == "" || want == quantityField(old, path...) {
			continue
		}
		if _, err := parseScaleQuantity("--"+field, want); err != nil {
			return fmt.Errorf("spec.primarySpec.resources.%s: %w", field, err)
		}
	}
	disksPath := append(append([]string{}, resourcesPath...), "disks")
	oldDisks, _, _ := unstructured.NestedSlice(old.Object, disksPath...)
	newDisks, _, _ := unstructured.NestedSlice(updated.Object, disksPath...)
	oldSizes := map[string]string{}
	for _, d := range oldDisks {
		if m, ok := d.(map[string]any); ok {
			oldSizes[fmt.Sprint(m["name"])] = fmt.Sprint(m["size"])
		}
	}
	for _, d := range newDisks {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		name := fmt.Sprint(m["name"])
		size := fmt.Sprint(m["size"])
		was, existed := oldSizes[name]
		if size != was {
			if _, err := parseScaleQuantity("--disk", size); err != nil {
				return fmt.Errorf("disk %s: %w", name, err)
			}

		}
		if !existed {
			continue
		}
		delete(oldSizes, name)
		before, err1 := resource.ParseQuantity(was)
		after, err2 := resource.ParseQuantity(size)
		if err1 == nil && err2 == nil && after.Cmp(before) < 0 {
			return fmt.Errorf("disk %s: %s is smaller than the current %s; disks can grow but not shrink", name, after.String(), was)
		}
	}
	for name := range oldSizes {
		return fmt.Errorf("disk %s was removed; adbo does not remove disks from a cluster", name)
	}
	return nil
}
