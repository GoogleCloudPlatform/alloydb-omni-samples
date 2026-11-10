package cmd

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func rowByLabel(t *testing.T, rows []editRow, label string) editRow {
	t.Helper()
	for _, r := range rows {
		if r.label == label {
			return r
		}
	}
	var labels []string
	for _, r := range rows {
		labels = append(labels, r.label)
	}
	t.Fatalf("no row %q in:\n  %s", label, strings.Join(labels, "\n  "))
	return editRow{}
}

func specValue(t *testing.T, s *editSession, path ...string) any {
	t.Helper()
	v, _, err := unstructured.NestedFieldNoCopy(s.object().Object, append([]string{"spec"}, path...)...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEditSessionListsSpecFieldsWithReadablePaths(t *testing.T) {
	s := newEditSession(readyDBCluster())
	rows := s.rows()
	for label, want := range map[string]any{
		"availability.numberOfStandbys":              int64(0),
		"availability.enableAutoHeal":                true,
		"primarySpec.resources.cpu":                  int64(2),
		"primarySpec.resources.memory":               "8Gi",
		"primarySpec.resources.disks[DataDisk].size": "10Gi",
		"primarySpec.parameters.max_connections":     "200",
	} {
		if r := rowByLabel(t, rows, label); r.value != want {
			t.Errorf("%s = %#v, want %#v", label, r.value, want)
		}
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].label > rows[i].label {
			t.Errorf("rows not sorted: %q before %q", rows[i-1].label, rows[i].label)
		}
	}
	if s.dirty() {
		t.Error("a new session has no changes")
	}
}

func TestEditSessionSetKeepsTheFieldsType(t *testing.T) {
	s := newEditSession(readyDBCluster())
	set := func(label, raw string) {
		t.Helper()
		if err := s.set(rowByLabel(t, s.rows(), label).path, raw); err != nil {
			t.Fatal(err)
		}
	}

	set("primarySpec.resources.cpu", "8")
	if v := specValue(t, s, "primarySpec", "resources", "cpu"); v != int64(8) {
		t.Errorf("cpu = %#v, want int64 8", v)
	}
	// cpu takes an int or a string; a value that isn't a number stays a string.
	set("primarySpec.resources.cpu", "500m")
	if v := specValue(t, s, "primarySpec", "resources", "cpu"); v != "500m" {
		t.Errorf("cpu = %#v, want \"500m\"", v)
	}
	set("availability.enableAutoHeal", "false")
	if v := specValue(t, s, "availability", "enableAutoHeal"); v != false {
		t.Errorf("enableAutoHeal = %#v, want false", v)
	}
	// Postgres parameters are strings, even when they look like numbers.
	set("primarySpec.parameters.max_connections", "300")
	if v := specValue(t, s, "primarySpec", "parameters", "max_connections"); v != "300" {
		t.Errorf("max_connections = %#v, want \"300\"", v)
	}
	// Quotes force a string.
	set("availability.numberOfStandbys", `"2"`)
	if v := specValue(t, s, "availability", "numberOfStandbys"); v != "2" {
		t.Errorf("numberOfStandbys = %#v, want \"2\"", v)
	}
}

func TestEditSessionTracksChanges(t *testing.T) {
	live := readyDBCluster()
	s := newEditSession(live)
	if err := s.set(rowByLabel(t, s.rows(), "primarySpec.resources.cpu").path, "8"); err != nil {
		t.Fatal(err)
	}
	if err := s.add("primarySpec.parameters.work_mem", "64MB"); err != nil {
		t.Fatal(err)
	}
	if err := s.remove(rowByLabel(t, s.rows(), "primarySpec.parameters.max_connections").path); err != nil {
		t.Fatal(err)
	}

	if !s.dirty() {
		t.Fatal("want dirty after three changes")
	}
	got := map[string]editRow{}
	for _, c := range s.changes() {
		got[c.label] = c
	}
	if c := got["primarySpec.resources.cpu"]; c.kind != changeModified || c.was != int64(2) || c.value != int64(8) {
		t.Errorf("cpu change = %+v", c)
	}
	if c := got["primarySpec.parameters.work_mem"]; c.kind != changeAdded || c.value != "64MB" {
		t.Errorf("work_mem change = %+v", c)
	}
	if c := got["primarySpec.parameters.max_connections"]; c.kind != changeRemoved || c.was != "200" {
		t.Errorf("max_connections change = %+v", c)
	}
	if len(got) != 3 {
		t.Errorf("want 3 changes, got %d: %+v", len(got), got)
	}
	// A removed field still has a row, so it can be restored.
	if r := rowByLabel(t, s.rows(), "primarySpec.parameters.max_connections"); r.kind != changeRemoved {
		t.Errorf("removed row kind = %v", r.kind)
	}

	obj := s.object()
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "status"); found {
		t.Error("object() must not send status")
	}
	if obj.GetManagedFields() != nil {
		t.Error("object() must not send managedFields")
	}
	if obj.GetResourceVersion() != live.GetResourceVersion() {
		t.Errorf("resourceVersion = %q, want %q so a concurrent change is a Conflict", obj.GetResourceVersion(), live.GetResourceVersion())
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(live.Object, "spec", "parameters", "work_mem"); found {
		t.Error("the session must not modify the live object")
	}
}

func TestEditSessionAddUsesStringsWhereSiblingsAreStrings(t *testing.T) {
	s := newEditSession(readyDBCluster())
	if err := s.add("primarySpec.parameters.shared_buffers", "256"); err != nil {
		t.Fatal(err)
	}
	if v := specValue(t, s, "primarySpec", "parameters", "shared_buffers"); v != "256" {
		t.Errorf("shared_buffers = %#v, want \"256\"", v)
	}
	if err := s.add("availability.healthcheckPeriodSeconds", "30"); err != nil {
		t.Fatal(err)
	}
	if v := specValue(t, s, "availability", "healthcheckPeriodSeconds"); v != int64(30) {
		t.Errorf("healthcheckPeriodSeconds = %#v, want int64 30", v)
	}
}

func TestEditSessionAddRejectsBadPaths(t *testing.T) {
	s := newEditSession(readyDBCluster())
	for path, want := range map[string]string{
		"":                                       "field name",
		"primarySpec..x":                         "field name",
		"primarySpec.resources.disks[0].x":       "$EDITOR",
		"primarySpec.resources.cpu":              "already set",
		"primarySpec.resources.memory.x":         "not a map",
		"primarySpec.parameters.max_connections": "already set",
	} {
		err := s.add(path, "1")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("add(%q): want error containing %q, got %v", path, want, err)
		}
	}
	if s.dirty() {
		t.Error("rejected adds must not change anything")
	}
}

func TestEditSessionRevertUndoesEachKindOfChange(t *testing.T) {
	s := newEditSession(readyDBCluster())
	cpu := rowByLabel(t, s.rows(), "primarySpec.resources.cpu").path
	maxConn := rowByLabel(t, s.rows(), "primarySpec.parameters.max_connections").path
	if err := s.set(cpu, "8"); err != nil {
		t.Fatal(err)
	}
	if err := s.remove(maxConn); err != nil {
		t.Fatal(err)
	}
	// Adding under a map that doesn't exist yet creates it.
	if err := s.add("features.googleMLExtension.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	added := rowByLabel(t, s.rows(), "features.googleMLExtension.enabled").path

	for _, p := range []fieldPath{cpu, maxConn, added} {
		s.revert(p)
	}
	if s.dirty() {
		t.Errorf("want no changes after reverting all, got %+v", s.changes())
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(s.object().Object, "spec", "features"); found {
		t.Error("reverting the add must also drop the maps it created")
	}
}

func TestEditSessionRebaseReappliesChangesOnFreshObject(t *testing.T) {
	s := newEditSession(readyDBCluster())
	if err := s.set(rowByLabel(t, s.rows(), "primarySpec.resources.cpu").path, "8"); err != nil {
		t.Fatal(err)
	}

	fresh := readyDBCluster()
	fresh.SetResourceVersion("5000")
	_ = unstructured.SetNestedField(fresh.Object, "128Gi", "spec", "primarySpec", "resources", "memory")
	if dropped := s.rebase(fresh); dropped != 0 {
		t.Errorf("dropped %d changes, want 0", dropped)
	}

	obj := s.object()
	if obj.GetResourceVersion() != "5000" {
		t.Errorf("resourceVersion = %q, want the fresh one", obj.GetResourceVersion())
	}
	if v := specValue(t, s, "primarySpec", "resources", "cpu"); v != int64(8) {
		t.Errorf("cpu = %#v, want my 8 re-applied", v)
	}
	if v := specValue(t, s, "primarySpec", "resources", "memory"); v != "128Gi" {
		t.Errorf("memory = %#v, want the fresh 128Gi", v)
	}
	if n := len(s.changes()); n != 1 {
		t.Errorf("want only my change left, got %+v", s.changes())
	}
}
