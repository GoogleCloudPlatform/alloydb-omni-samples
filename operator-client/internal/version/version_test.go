package version

import "testing"

func TestGetPrefersLinkerVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })

	Version = "v0.3.0"
	if got := Get().Version; got != "v0.3.0" {
		t.Fatalf("Get().Version = %q, want the -ldflags value v0.3.0", got)
	}
}

func TestGetFallsBackWhenUnset(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })

	Version = ""
	info := Get()
	if info.Version == "" {
		t.Fatal("Get().Version is empty; a local build should still report something")
	}
	if info.GoVersion == "" || info.Platform == "" {
		t.Fatalf("Get() = %+v, want GoVersion and Platform filled", info)
	}
}
