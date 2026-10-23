package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootDropsKubectlClusterAndRequestTimeout(t *testing.T) {
	root := NewRootCommand()
	if root.PersistentFlags().Lookup("cluster") != nil {
		t.Fatal("kubectl --cluster should not be registered")
	}
	if root.PersistentFlags().Lookup("request-timeout") != nil {
		t.Fatal("kubectl --request-timeout should not be registered")
	}
	if root.PersistentFlags().Lookup("timeout") == nil {
		t.Fatal("adbo --timeout should be registered")
	}
	if root.PersistentFlags().Lookup("namespace") == nil {
		t.Fatal("kubectl -n should still be registered")
	}
}

func TestRootHelpListsFlags(t *testing.T) {
	root := NewRootCommand()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"--timeout", "--output", "--namespace", "--yes", "cluster", "backup"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %s:\n%s", want, out)
		}
	}
}

func TestRootRejectsInvalidOutput(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"-o", "xml"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid --output to fail")
	}
}
