package cmd

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strconv"
)

func validateSpec(old, updated *unstructured.Unstructured) error {
	if updated == nil || updated.Object == nil {
		return fmt.Errorf("no updated DBCluster spec to validate")
	}
	if old == nil || old.Object == nil {
		old = &unstructured.Unstructured{Object: map[string]any{}}
	}
	if v, found, _ := unstructured.NestedFieldNoCopy(updated.Object, standbysPath...); found {
		n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		if err != nil {
			return fmt.Errorf("spec.availability.numberOfStandbys %v is not a number", v)
		}
		if err := checkStandbys(n); err != nil {
			return fmt.Errorf("spec.availability.numberOfStandbys: %w", err)
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
		if m, ok := d.(map[string]any); ok && diskField(m, "name") != "" {
			oldSizes[diskField(m, "name")] = diskField(m, "size")
		}
	}
	// seen tracks names separately from oldSizes so a disk listed twice
	// cannot slip its second entry past the shrink check.
	seen := map[string]bool{}
	for _, d := range newDisks {
		m, ok := d.(map[string]any)
		if !ok {
			return fmt.Errorf("spec.primarySpec.resources.disks: every entry must be a disk with a name and size")
		}
		name, size := diskField(m, "name"), diskField(m, "size")
		if name == "" {
			return fmt.Errorf("spec.primarySpec.resources.disks: every disk needs a name")
		}
		if seen[name] {
			return fmt.Errorf("disk %s is listed twice", name)
		}
		seen[name] = true
		if size == "" {
			return fmt.Errorf("disk %s needs a size", name)
		}
		was, existed := oldSizes[name]
		if size != was {
			if _, err := parseScaleQuantity("--disk", size); err != nil {
				return fmt.Errorf("disk %s: %w", name, err)
			}
		}
		if !existed || was == "" {
			continue
		}
		before, err1 := resource.ParseQuantity(was)
		after, err2 := resource.ParseQuantity(size)
		if err1 == nil && err2 == nil {
			if err := checkDiskGrow(name, before, after); err != nil {
				return err
			}
		}
	}
	for name := range oldSizes {
		if !seen[name] {
			return fmt.Errorf("disk %s was removed; adbo does not remove disks from a cluster", name)
		}
	}
	return nil
}

func checkStandbys(n int64) error {
	const maxStandbys = 5
	if n < 0 || n > maxStandbys {
		return fmt.Errorf("standbys must be between 0 and %d, got %d", maxStandbys, n)
	}
	return nil
}

func checkDiskGrow(name string, before, after resource.Quantity) error {
	if after.Cmp(before) < 0 {
		return fmt.Errorf("disk %s: %s is smaller than the current %s; disks can grow but not shrink", name, after.String(), before.String())
	}
	return nil
}

// diskField reads a disk's name or size as text, or "" when it is missing or
// null, so an omitted field is not mistaken for the text "<nil>".
func diskField(disk map[string]any, key string) string {
	v, ok := disk[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
