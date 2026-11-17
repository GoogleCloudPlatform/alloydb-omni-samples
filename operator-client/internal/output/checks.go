package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Check is one row of a pass/fail report (adbo doctor).
type Check struct {
	Name, Status, Detail string
}

// PrintChecks writes a CHECK / STATUS / DETAIL table.
// Newlines and tabs in Detail are flattened so one check stays on one row.
func PrintChecks(w io.Writer, checks []Check) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSTATUS\tDETAIL")
	if len(checks) == 0 {
		fmt.Fprintln(tw, "(none)\t\t")
	}
	for _, c := range checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, c.Status, oneLine(c.Detail))
	}
	return tw.Flush()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}
