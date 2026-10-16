package util

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"sigs.k8s.io/yaml"
)

// Table is what a command shows in the default (human) format.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Print writes either the table (for humans) or the raw objects (for scripts), based on -o.
func Print(out io.Writer, format string, table Table, raw any) error {
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ") // pretty-print with 2-space indent
		return enc.Encode(raw)
	case "yaml":
		b, err := yaml.Marshal(raw) // sigs.k8s.io/yaml converts via JSON, so Kubernetes objects print correctly
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	default: // "table"
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0) // pads columns so they line up
		fmt.Fprintln(w, strings.Join(table.Headers, "\t"))
		for _, row := range table.Rows {
			fmt.Fprintln(w, strings.Join(row, "\t")) // \t separates columns
		}
		return w.Flush() // tabwriter buffers; Flush actually writes
	}
}