package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpListsDiagnostics(t *testing.T) {
	root := NewRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Provisioning",
		"Scaling",
		"Backup, restore, and replication",
		"Management",
		"Miscellaneous",
		"adbo cluster create <name>",
		"adbo cluster delete <name>",
		"adbo cluster stop [<name>]",
		"adbo cluster start [<name>]",
		"adbo cluster restart [<name>]",
		"adbo cluster maintenance [<name>]",
		"adbo cluster upgrade [<name>]",
		"adbo cluster downgrade [<name>]",
		"adbo cluster scale [<name>]",
		"adbo cluster read-pool create",
		"adbo cluster read-pool list",
		"adbo cluster read-pool scale",
		"adbo cluster read-pool delete",
		"adbo backup trigger [<name>]",
		"adbo backup list [<name>]",
		"adbo restore [<name>]",
		"adbo failover [<name>]",
		"adbo switchover [<name>]",
		"adbo replicate create <source> <target>",
		"adbo replicate status <name>",
		"adbo replicate promote <target>",
		"adbo replicate delete <name>",
		"adbo cluster credentials [<name>]",
		"adbo cluster list",
		"adbo cluster select [<name>]",
		"adbo status [<name>]",
		"adbo cluster status [<name>]",
		"adbo cluster describe [<name>]",
		"adbo cluster edit [<name>]",
		"adbo logs [<name>]",
		"adbo support export [<name>]",
		"adbo version",
		"adbo connect [<name>]",
		"adbo doctor",
		"adbo completion [shell]",
		"adbo help [command]",
		"Client version",
		"--timeout",
		"--namespace",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[1m") {
		t.Fatal("piped help should leave section titles plain")
	}
	if strings.Contains(out, "--version") || strings.Contains(out, "--wait") || strings.Contains(out, "--read-only") {
		t.Fatalf("catalog should list arguments, not flags:\n%s", out)
	}
}

func TestHelpUnknownCommand(t *testing.T) {
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"help", "not-a-command"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected unknown command")
	}
}

func TestHelpSpecCommand(t *testing.T) {
	root := NewRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"help", "cluster", "create"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "adbo cluster create <name>") || !strings.Contains(out, "DBClusterReady") {
		t.Fatalf("spec help:\n%s", out)
	}
}

func TestConnectHelpShowsFlags(t *testing.T) {
	root := NewRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"connect", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"--username", "--sslmode", "--pod", "--local-port", "--port", "--read-only", "--exec"} {
		if !strings.Contains(out, want) {
			t.Fatalf("connect help missing %s:\n%s", want, out)
		}
	}
}
