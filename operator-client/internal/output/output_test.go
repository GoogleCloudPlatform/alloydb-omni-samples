package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintJSONNilIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(&buf, OutputJSON, Table{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Fatalf("json nil raw: got %q, want []", got)
	}
}

func TestPrintJSONTypedNilSlice(t *testing.T) {
	var items []string
	var buf bytes.Buffer
	if err := Print(&buf, OutputJSON, Table{}, items); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Fatalf("got %q", got)
	}
}

func TestPrintTableEmptyShowsNone(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(&buf, OutputTable, Table{Headers: []string{"NAME"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(none)") {
		t.Fatalf("empty table: got %q", buf.String())
	}
}

func TestPrintUnknownFormat(t *testing.T) {
	if err := Print(&bytes.Buffer{}, "xml", Table{}, nil); err == nil {
		t.Fatal("unknown format should error")
	}
}
