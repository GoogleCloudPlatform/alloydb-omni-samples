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
