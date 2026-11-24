//go:build e2e

// End-to-end tests for `adbo restore`. They build the real adbo binary and
// run it against a live cluster, so they are opt-in:
//
//	ADBO_E2E_NAMESPACE=adbo-eva \
//	ADBO_E2E_CLUSTER=e2e-db \
//	ADBO_E2E_BACKUP=e2e-db-backup-abc12 \
//	go test -tags e2e -v -timeout 60m ./e2e/
//
// WARNING: these tests restore ADBO_E2E_CLUSTER in place, which replaces all
// of its data. Point them at a throwaway cluster, never one you care about.
// ADBO_E2E_BACKUP must be a Succeeded backup of that cluster.
// ADBO_E2E_CONTEXT (optional) selects a kube-context; default is the current one.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// restoreTimeout bounds each full restore. Restores of small test clusters
// usually take a few minutes; this leaves plenty of room.
const restoreTimeout = 20 * time.Minute

// adboBin is the binary built once in TestMain and run by every test.
var adboBin string

// TestMain builds adbo into a temp dir before any test runs, so the tests
// exercise exactly what a user would run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "adbo-e2e-")
	if err != nil {
		panic(err)
	}
	adboBin = filepath.Join(dir, "adbo")
	build := exec.Command("go", "build", "-o", adboBin, "./cmd/adbo")
	build.Dir = ".." // the operator-client module root
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building adbo: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env holds the test settings read from ADBO_E2E_* variables.
type env struct {
	namespace, cluster, backup, kubeContext string
}

// loadEnv reads the settings, skipping the test when they are not set.
func loadEnv(t *testing.T) env {
	t.Helper()
	e := env{
		namespace:   os.Getenv("ADBO_E2E_NAMESPACE"),
		cluster:     os.Getenv("ADBO_E2E_CLUSTER"),
		backup:      os.Getenv("ADBO_E2E_BACKUP"),
		kubeContext: os.Getenv("ADBO_E2E_CONTEXT"),
	}
	if e.namespace == "" || e.cluster == "" || e.backup == "" {
		t.Skip("set ADBO_E2E_NAMESPACE, ADBO_E2E_CLUSTER and ADBO_E2E_BACKUP to run e2e tests")
	}
	return e
}

// result is what one adbo run produced.
type result struct {
	stdout, stderr string
	exitCode       int
}

// adbo runs the binary with args plus -n (and --context when set). stdin is
// fed to the process, for answering confirmation prompts. Each run gets its
// own config dir so a `cluster select` on this machine cannot leak in.
func (e env) adbo(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	args = append(args, "-n", e.namespace)
	if e.kubeContext != "" {
		args = append(args, "--context", e.kubeContext)
	}
	cmd := exec.Command(adboBin, args...)
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut

	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errOut.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running adbo %v: %v", args, err)
	}
	// Always log stderr: it shows the restore phases the operator reports,
	// which is how we learn the real phase names.
	t.Logf("$ adbo %s\nexit=%d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.exitCode, r.stdout, r.stderr)
	return r
}

// clients connects to the same cluster the binary uses, so tests can check
// what adbo did directly through the API.
func (e env) clients(t *testing.T) *k8s.Clients {
	t.Helper()
	flags := genericclioptions.NewConfigFlags(true)
	flags.Namespace = &e.namespace
	if e.kubeContext != "" {
		flags.Context = &e.kubeContext
	}
	c, err := k8s.New(flags)
	if err != nil {
		t.Fatalf("connecting to cluster: %v", err)
	}
	return c
}

// restoreNames lists the Restore objects for the test cluster.
func (e env) restoreNames(t *testing.T, c *k8s.Clients) map[string]bool {
	t.Helper()
	list, err := c.In(k8s.RestoreGVR).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing restores: %v", err)
	}
	names := map[string]bool{}
	for _, r := range list.Items {
		if k8s.StringField(&r, "spec", "sourceDBCluster") == e.cluster {
			names[r.GetName()] = true
		}
	}
	return names
}

// waitUntil polls check every 5s until it returns true or timeout passes.
func waitUntil(t *testing.T, timeout time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Second)
	}
}

// clusterReady reports whether the test cluster is back to DBClusterReady.
func (e env) clusterReady(c *k8s.Clients) bool {
	db, err := c.In(k8s.DBClusterGVR).Get(context.Background(), e.cluster, metav1.GetOptions{})
	return err == nil && k8s.StringField(db, "status", "phase") == "DBClusterReady"
}

// restoreFinished reports whether the named Restore has a completeTime.
func restoreFinished(c *k8s.Clients, name string) bool {
	r, err := c.In(k8s.RestoreGVR).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil && k8s.StringField(r, "status", "completeTime") != "-"
}

// TestRestore runs the cases in order: the ones that change nothing first,
// then the real restores. Subtests run sequentially, so two restores never
// overlap on the same cluster.
func TestRestore(t *testing.T) {
	e := loadEnv(t)
	c := e.clients(t)

	// Every case below starts from a healthy cluster.
	waitUntil(t, restoreTimeout, "cluster "+e.cluster+" to be DBClusterReady", func() bool { return e.clusterReady(c) })

	t.Run("missing --backup is rejected", func(t *testing.T) {
		r := e.adbo(t, "", "restore", e.cluster)
		if r.exitCode == 0 || !strings.Contains(r.stderr, "--backup is required") {
			t.Errorf("want a non-zero exit and '--backup is required'")
		}
	})

	t.Run("unknown backup is rejected", func(t *testing.T) {
		r := e.adbo(t, "", "restore", e.cluster, "--backup", "no-such-backup-e2e", "-y")
		if r.exitCode == 0 || !strings.Contains(r.stderr, "not found") {
			t.Errorf("want a non-zero exit and 'not found'")
		}
	})

	t.Run("unknown cluster is rejected", func(t *testing.T) {
		r := e.adbo(t, "", "restore", "no-such-cluster-e2e", "--backup", e.backup, "-y")
		if r.exitCode == 0 {
			t.Errorf("want a non-zero exit for a cluster that does not exist")
		}
	})

	t.Run("declining the prompt changes nothing", func(t *testing.T) {
		before := e.restoreNames(t, c)
		r := e.adbo(t, "n\n", "restore", e.cluster, "--backup", e.backup)
		if r.exitCode != 1 {
			t.Errorf("exit code = %d, want 1 so scripts see the cancel", r.exitCode)
		}
		if !strings.Contains(r.stderr, "overwrite ALL data") {
			t.Errorf("prompt should warn about overwriting data")
		}
		if after := e.restoreNames(t, c); len(after) != len(before) {
			t.Errorf("a Restore was created even though the user said no")
		}
	})

	t.Run("restore waits and prints JSON", func(t *testing.T) {
		r := e.adbo(t, "", "restore", e.cluster, "--backup", e.backup, "-y", "-o", "json",
			"--timeout", restoreTimeout.String())
		if r.exitCode != 0 {
			t.Fatalf("restore failed; see stderr above")
		}
		// stdout must be only the Restore object, so it parses as JSON.
		var obj map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &obj); err != nil {
			t.Fatalf("stdout is not valid JSON: %v", err)
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetKind() != "Restore" {
			t.Errorf("kind = %q, want Restore", u.GetKind())
		}
		if got := k8s.StringField(u, "spec", "backup"); got != e.backup {
			t.Errorf("spec.backup = %q, want %q", got, e.backup)
		}
		if k8s.StringField(u, "status", "completeTime") == "-" {
			t.Errorf("adbo returned before the restore had a completeTime")
		}
		// Progress lines go to stderr, never stdout.
		if !strings.Contains(r.stderr, "Waiting for restore") {
			t.Errorf("want progress on stderr")
		}
		waitUntil(t, restoreTimeout, "cluster to be ready after restore", func() bool { return e.clusterReady(c) })
	})

	t.Run("--no-wait returns at once", func(t *testing.T) {
		before := e.restoreNames(t, c)
		start := time.Now()
		r := e.adbo(t, "", "restore", e.cluster, "--backup", e.backup, "-y", "--no-wait")
		if r.exitCode != 0 {
			t.Fatalf("restore --no-wait failed; see stderr above")
		}
		if took := time.Since(start); took > time.Minute {
			t.Errorf("--no-wait took %s; it should not wait for the restore", took)
		}
		if !strings.Contains(r.stdout, "Started restore") {
			t.Errorf("want 'Started restore' on stdout")
		}
		// Find the Restore this run created, then let it finish so the
		// cluster is not left mid-restore when the tests end.
		var created string
		for name := range e.restoreNames(t, c) {
			if !before[name] {
				created = name
			}
		}
		if created == "" {
			t.Fatalf("no new Restore object was created")
		}
		waitUntil(t, restoreTimeout, "restore "+created+" to finish", func() bool { return restoreFinished(c, created) })
		waitUntil(t, restoreTimeout, "cluster to be ready after restore", func() bool { return e.clusterReady(c) })
	})
}
