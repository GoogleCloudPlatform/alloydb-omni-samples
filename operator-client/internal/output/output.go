// Package output renders command results as a table, JSON, or YAML.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"sigs.k8s.io/yaml"
)

// Formats accepted by --output / -o. Keep in sync with root PersistentPreRunE.
const (
	OutputTable = "table"
	OutputJSON  = "json"
	OutputYAML  = "yaml"
)

// Table is the human-readable view of a list command.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Print writes table (default), JSON, or YAML. A nil raw value prints as []
// so scripts never see a JSON null.
func Print(out io.Writer, format string, table Table, raw any) error {
	if raw == nil {
		raw = []any{}
	}
	switch format {
	case OutputJSON:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(raw)
	case OutputYAML:
		b, err := yaml.Marshal(raw)
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	case OutputTable, "":
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, strings.Join(table.Headers, "\t"))
		if len(table.Rows) == 0 {
			fmt.Fprintln(w, "(none)")
		} else {
			for _, row := range table.Rows {
				fmt.Fprintln(w, strings.Join(row, "\t"))
			}
		}
		return w.Flush()
	default:
		return fmt.Errorf("unsupported output format: %q", format)
	}
}
