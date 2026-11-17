package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintChecksFlattensDetail(t *testing.T) {
	var buf bytes.Buffer
	err := PrintChecks(&buf, []Check{{
		Name:   "nodes",
		Status: "WARN",
		Detail: "no nodes yet.\nOn GKE Autopilot this is normal",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "CHECK") || !strings.Contains(got, "WARN") {
		t.Fatalf("table: %s", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("expected a header and one row, got %q", got)
	}
}
