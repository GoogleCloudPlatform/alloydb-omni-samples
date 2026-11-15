package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SelectedCluster("ctx", "ns"); got != "" {
		t.Fatalf("empty config returned %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{}
	c.SetSelectedCluster("ctx-a", "default", "db1")
	c.SetSelectedCluster("ctx-a", "prod", "db2")
	c.SetSelectedCluster("ctx-b", "default", "db3")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	path, _ := Path()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file should be 0600: %v %v", info, err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ctx, ns, want string }{
		{"ctx-a", "default", "db1"},
		{"ctx-a", "prod", "db2"},
		{"ctx-b", "default", "db3"},
		{"ctx-b", "prod", ""},
	} {
		if s := got.SelectedCluster(tc.ctx, tc.ns); s != tc.want {
			t.Errorf("SelectedCluster(%q, %q) = %q, want %q", tc.ctx, tc.ns, s, tc.want)
		}
	}
}

func TestClearSelectedCluster(t *testing.T) {
	c := &Config{}
	c.SetSelectedCluster("ctx", "ns", "db")
	c.SetSelectedCluster("ctx", "ns", "")
	if len(c.SelectedClusters) != 0 {
		t.Fatalf("clearing the last entry should drop the context: %v", c.SelectedClusters)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _ := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("selectedClusters: [not, a, map]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a corrupt config")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	c := &Config{}
	c.SetSelectedCluster("ctx", "ns", "db")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "adbo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Fatalf("want only config.yaml, got %v", entries)
	}
	info, err := os.Stat(filepath.Join(dir, "adbo", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
}
