package cmd

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestValidateSpec(t *testing.T) {
	setDisks := func(db *unstructured.Unstructured, disks ...map[string]any) {
		list := make([]any, len(disks))
		for i, d := range disks {
			list[i] = d
		}
		_ = unstructured.SetNestedSlice(db.Object, list, append(append([]string{}, resourcesPath...), "disks")...)
	}
	disk := func(name, size string) map[string]any { return map[string]any{"name": name, "size": size} }
	set := func(db *unstructured.Unstructured, v any, path ...string) {
		_ = unstructured.SetNestedField(db.Object, v, path...)
	}
	resource := func(field string) []string { return append(append([]string{}, resourcesPath...), field) }

	tests := []struct {
		name   string
		change func(db *unstructured.Unstructured)
		want   string // "" means valid
	}{
		{"no change", func(*unstructured.Unstructured) {}, ""},

		{"standbys in range", func(db *unstructured.Unstructured) { set(db, int64(2), standbysPath...) }, ""},
		{"standbys as text", func(db *unstructured.Unstructured) { set(db, "3", standbysPath...) }, ""},
		{"too many standbys", func(db *unstructured.Unstructured) { set(db, int64(6), standbysPath...) }, "between 0 and 5"},
		{"negative standbys", func(db *unstructured.Unstructured) { set(db, int64(-1), standbysPath...) }, "between 0 and 5"},
		{"standbys not a number", func(db *unstructured.Unstructured) { set(db, "lots", standbysPath...) }, "is not a number"},

		{"more cpu", func(db *unstructured.Unstructured) { set(db, int64(4), resource("cpu")...) }, ""},
		{"millicores", func(db *unstructured.Unstructured) { set(db, "500m", resource("cpu")...) }, ""},
		{"cpu with a byte unit", func(db *unstructured.Unstructured) { set(db, "4Gi", resource("cpu")...) }, "resources.cpu"},
		{"zero cpu", func(db *unstructured.Unstructured) { set(db, int64(0), resource("cpu")...) }, "not a valid positive size"},
		{"more memory", func(db *unstructured.Unstructured) { set(db, "32Gi", resource("memory")...) }, ""},
		{"unitless memory", func(db *unstructured.Unstructured) { set(db, "16", resource("memory")...) }, "add a unit"},
		{"too little memory", func(db *unstructured.Unstructured) { set(db, "512Mi", resource("memory")...) }, "at least 1Gi"},

		{"grow a disk", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "50Gi"), disk("LogDisk", "5Gi"))
		}, ""},
		{"same size, other spelling", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "10240Mi"), disk("LogDisk", "5Gi"))
		}, ""},
		{"add a disk", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "10Gi"), disk("LogDisk", "5Gi"), disk("BackupDisk", "20Gi"))
		}, ""},
		{"shrink a disk", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "5Gi"), disk("LogDisk", "5Gi"))
		}, "can grow but not shrink"},
		{"shrink the second disk", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "10Gi"), disk("LogDisk", "1Gi"))
		}, "disk LogDisk"},
		{"remove a disk", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "10Gi"))
		}, "disk LogDisk was removed"},
		{"new disk without a unit", func(db *unstructured.Unstructured) {
			setDisks(db, disk("DataDisk", "10Gi"), disk("LogDisk", "5Gi"), disk("BackupDisk", "100"))
		}, "add a unit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := sizedCluster("my-db") // cpu 2, memory 16Gi, DataDisk 10Gi, LogDisk 5Gi
			updated := old.DeepCopy()
			tc.change(updated)
			err := validateSpec(old, updated)
			if tc.want == "" && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateSpecOnlyJudgesChangedResources(t *testing.T) {
	// A cluster that already has an odd value must still allow unrelated edits.
	old := sizedCluster("my-db")
	_ = unstructured.SetNestedField(old.Object, "16", append(append([]string{}, resourcesPath...), "memory")...)
	updated := old.DeepCopy()
	_ = unstructured.SetNestedField(updated.Object, int64(4), append(append([]string{}, resourcesPath...), "cpu")...)
	if err := validateSpec(old, updated); err != nil {
		t.Fatalf("unchanged memory should not be checked, got %v", err)
	}
}

func TestValidateSpecWithoutOld(t *testing.T) {
	// No old cluster (e.g. create): everything set counts as new and is checked.
	if err := validateSpec(nil, sizedCluster("my-db")); err != nil {
		t.Fatalf("valid new cluster rejected: %v", err)
	}
	bad := sizedCluster("my-db")
	_ = unstructured.SetNestedField(bad.Object, "16", append(append([]string{}, resourcesPath...), "memory")...)
	if err := validateSpec(nil, bad); err == nil || !strings.Contains(err.Error(), "add a unit") {
		t.Fatalf("err = %v, want the unitless memory error", err)
	}
}
