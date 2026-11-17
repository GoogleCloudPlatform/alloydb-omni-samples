package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version"
)

// installHelp makes `adbo`, `adbo --help`, and `adbo help` share one catalog.
// Subcommand --help still prints that command's flags and examples.
func installHelp(root *cobra.Command) {
	root.SetHelpCommand(newHelpCommand())
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		out := cmd.OutOrStdout()
		if cmd.Parent() == nil {
			if err := printCatalog(out); err != nil {
				cmd.PrintErrln(err)
			}
			return
		}
		if err := printCommandHelp(cmd, out); err != nil {
			cmd.PrintErrln(err)
		}
	})
}

func newHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "List available commands",
		Long:  "List adbo commands. Pass a command to see its flags and examples.",
		Example: "  adbo help\n" +
			"  adbo help connect\n" +
			"  adbo help support export",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 || strings.Join(args, " ") == "help" {
				return printCatalog(out)
			}
			if target := commandAt(cmd.Root(), args); target != nil {
				return target.Help()
			}
			if spec, ok := findSpec(args); ok {
				return printSpecHelp(out, spec)
			}
			return fmt.Errorf("unknown command %q", strings.Join(args, " "))
		},
	}
}

func printCatalog(out io.Writer) error {
	fmt.Fprintln(out, "AlloyDB Omni CLI (adbo)")
	fmt.Fprintln(out)
	for _, section := range specSections() {
		var rows []specCommand
		width := 0
		for _, cmd := range specCommands {
			if cmd.section != section {
				continue
			}
			rows = append(rows, cmd)
			if len(cmd.usage) > width {
				width = len(cmd.usage)
			}
		}
		fmt.Fprintln(out, boldIfTerminal(out, section))
		for _, row := range rows {
			fmt.Fprintf(out, "  %-*s  %s\n", width, row.usage, row.short)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "Client version: %s\n\n", version.Version)
	fmt.Fprintln(out, "Global flags: -n, --namespace, --context, --kubeconfig, -o, --output, --timeout, -y, --yes")
	fmt.Fprintln(out, `Use "adbo help <command>" for flags and examples.`)
	return nil
}

// boldIfTerminal wraps a section title in ANSI bold when out is a terminal.
// A pipe or test buffer stays plain text.
func boldIfTerminal(out io.Writer, text string) string {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return text
	}
	return "\033[1m" + text + "\033[0m"
}

// commandAt returns the cobra command only when args name that command exactly.
// A parent such as `cluster` with a leftover `create` is not a match.
func commandAt(root *cobra.Command, args []string) *cobra.Command {
	target, rest, err := root.Find(args)
	if err != nil || target == nil || target == root || len(rest) != 0 || target.Name() == "help" {
		return nil
	}
	return target
}

func printSpecHelp(out io.Writer, spec specCommand) error {
	fmt.Fprintln(out, spec.short)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintf(out, "  %s\n", spec.usage)
	return nil
}

func printCommandHelp(cmd *cobra.Command, out io.Writer) error {
	text := cmd.Long
	if text == "" {
		text = cmd.Short
	}
	text = strings.TrimRight(text, " \n\t")
	if text != "" {
		fmt.Fprintln(out, text)
		fmt.Fprintln(out)
	}
	if cmd.Runnable() || cmd.HasSubCommands() {
		fmt.Fprint(out, cmd.UsageString())
	}
	return nil
}
