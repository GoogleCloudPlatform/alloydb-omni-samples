package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

const (
	pgLog       = "/obs/diagnostic/postgresql.log"
	internalLog = "/obs/diagnostic/postgresql.internal"
)

// fakePods stands in for the database containers: a small filesystem that
// answers the tail/head/cat commands adbo runs, plus container stdout.
// `tail -F` prints its lines and then blocks until ctx ends, like the real one.
type fakePods struct {
	mu       sync.Mutex
	files    map[string]map[string]string // pod -> path -> content
	stdout   map[string]string            // pod -> database container log
	stderr   map[string]string            // pod -> what the command prints to stderr first
	execs    [][]string                   // pod + command, in call order
	logOpts  []corev1.PodLogOptions
	execErrs map[string]error // pod -> error to return
}

func (f *fakePods) Exec(ctx context.Context, pod, container string, command []string, stdout, stderr io.Writer) error {
	f.mu.Lock()
	f.execs = append(f.execs, append([]string{pod}, command...))
	execErr, msg := f.execErrs[pod], f.stderr[pod]
	path := command[len(command)-1]
	content, ok := f.files[pod][path]
	f.mu.Unlock()

	if container != k8s.DatabaseContainer {
		return fmt.Errorf("container %q not found", container)
	}
	if msg != "" {
		io.WriteString(stderr, msg)
	}
	if execErr != nil {
		return execErr
	}
	follow := slices.Contains(command, "-F")
	if !ok {
		if follow { // tail -F waits for the file to appear
			<-ctx.Done()
			return ctx.Err()
		}
		return fmt.Errorf("%s: No such file or directory", path)
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	switch command[0] {
	case "cat":
	case "tail", "head":
		if command[1] != "-n" {
			return fmt.Errorf("unexpected %v", command)
		}
		n, err := strconv.Atoi(command[2])
		if err != nil {
			return err
		}
		if command[0] == "tail" && n < len(lines) {
			lines = lines[len(lines)-n:]
		}
		if command[0] == "head" && n < len(lines) {
			lines = lines[:n]
		}
	default:
		return fmt.Errorf("unexpected command %v", command)
	}
	if _, err := io.WriteString(stdout, strings.Join(lines, "")); err != nil {
		return err
	}
	if follow {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakePods) ContainerLogs(_ context.Context, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logOpts = append(f.logOpts, *opts)
	return io.NopCloser(strings.NewReader(f.stdout[pod])), nil
}

// syncBuffer is safe to read while the command is still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startLogs runs `adbo logs` in the background. stop() is the user pressing
// Ctrl+C; it returns the command's error.
func startLogs(t *testing.T, ctx context.Context, opts *GlobalOptions, args ...string) (out *syncBuffer, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	cmd := newLogsCommand(opts)
	out = &syncBuffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	return out, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("adbo logs did not stop after Ctrl+C")
			return nil
		}
	}
}

func waitForOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in:\n%s", want, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func dbPod(name, cluster, taskType string, running bool) *corev1.Pod {
	state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	phase := corev1.PodRunning
	if !running {
		state = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "omni-test",
			Labels: map[string]string{
				"alloydbomni.internal.dbadmin.goog/dbcluster": cluster,
				"alloydbomni.internal.dbadmin.goog/task-type": taskType,
			},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "database"}, {Name: "logrotate-agent"}}},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "database", State: state}},
		},
	}
}

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	return b.String()
}

// logsEnv is a ready cluster "big-db" with one running primary pod, plus
// pods that must never be read: another cluster's and a backup pod.
func logsEnv(t *testing.T, extraPods ...runtime.Object) (*GlobalOptions, *fakePods) {
	t.Helper()
	cluster := stuckDBCluster()
	_ = unstructured.SetNestedField(cluster.Object, "DBClusterReady", "status", "phase")
	pods := []runtime.Object{
		dbPod("al-c374-big-db-0", "big-db", "database", true),
		dbPod("al-9f1e-other-db-0", "other-db", "database", true),
		dbPod("big-db-backuprepo-0", "big-db", "backuprepo", true),
	}
	opts := fakeClusterOpts([]runtime.Object{cluster}, append(pods, extraPods...)...)

	fake := &fakePods{
		files: map[string]map[string]string{
			"al-c374-big-db-0": {pgLog: lines(150, "pg"), internalLog: lines(3, "internal")},
			"al-c374-big-db-1": {pgLog: lines(2, "standby-pg")},
		},
		stdout: map[string]string{"al-c374-big-db-0": "dbdaemon started\n"},
	}
	old := newPodLogReader
	t.Cleanup(func() { newPodLogReader = old })
	newPodLogReader = func(*k8s.Clients) podLogReader { return fake }
	return opts, fake
}

func TestLogsDefaultsToLast100PostgresLines(t *testing.T) {
	opts, fake := logsEnv(t)

	out, err := runCmd(t, newLogsCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "pg line 150\n") || !strings.Contains(out, "pg line 51\n") || strings.Contains(out, "pg line 50\n") {
		t.Fatalf("want lines 51-150 of postgresql.log:\n%s", out)
	}
	if strings.Contains(out, "[") {
		t.Fatalf("a single pod should not get a prefix:\n%s", out)
	}
	for _, e := range fake.execs {
		if e[0] != "al-c374-big-db-0" {
			t.Fatalf("read pod %s, which is not a big-db database pod", e[0])
		}
	}
}

func TestLogsSources(t *testing.T) {
	opts, fake := logsEnv(t)

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--source", "internal")
	if err != nil || !strings.Contains(out, "internal line 3") {
		t.Fatalf("--source internal: %v\n%s", err, out)
	}

	out, err = runCmd(t, newLogsCommand(opts), "big-db", "--source", "dbdaemon")
	if err != nil || !strings.Contains(out, "dbdaemon started") {
		t.Fatalf("--source dbdaemon: %v\n%s", err, out)
	}
	if len(fake.logOpts) != 1 || fake.logOpts[0].Container != "database" {
		t.Fatalf("dbdaemon should read the database container's log, got %+v", fake.logOpts)
	}

	if _, err := runCmd(t, newLogsCommand(opts), "big-db", "--source", "nginx"); err == nil {
		t.Fatal("unknown --source should fail")
	}
}

func TestLogsTailHeadAndAll(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		want, not string
		wantLines int
	}{
		{[]string{"--tail", "2"}, "pg line 149\npg line 150\n", "pg line 148", 2},
		{[]string{"-t", "-1"}, "pg line 1\n", "", 150},
		{[]string{"--head", "2"}, "pg line 1\npg line 2\n", "pg line 3", 2},
	} {
		opts, _ := logsEnv(t)
		out, err := runCmd(t, newLogsCommand(opts), append([]string{"big-db"}, tc.args...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.args, err, out)
		}
		if !strings.Contains(out, tc.want) || (tc.not != "" && strings.Contains(out, tc.not)) {
			t.Fatalf("%v: unexpected output:\n%s", tc.args, out)
		}
		if got := strings.Count(out, "\n"); got != tc.wantLines {
			t.Fatalf("%v: %d lines, want %d", tc.args, got, tc.wantLines)
		}
	}
}

func TestLogsFollowUsesTailFUntilCtrlC(t *testing.T) {
	opts, fake := logsEnv(t)

	out, stop := startLogs(t, context.Background(), opts, "big-db", "-f", "--tail", "10")
	waitForOutput(t, out, "pg line 150\n")
	if err := stop(); err != nil {
		t.Fatalf("Ctrl+C should end follow cleanly, got %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := []string{"al-c374-big-db-0", "tail", "-n", "10", "-F", pgLog}
	if len(fake.execs) != 1 || !slices.Equal(fake.execs[0], want) {
		t.Fatalf("follow ran %v, want %v", fake.execs, want)
	}
}

func TestLogsFollowShowsStderrAsItHappens(t *testing.T) {
	opts, fake := logsEnv(t)
	delete(fake.files["al-c374-big-db-0"], pgLog)
	fake.stderr = map[string]string{"al-c374-big-db-0": "tail: cannot open '/obs/diagnostic/postgresql.log' for reading: No such file or directory\n"}

	out, stop := startLogs(t, context.Background(), opts, "big-db", "-f")
	waitForOutput(t, out, "cannot open '/obs/diagnostic/postgresql.log'") // while still following
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLogsFollowReportsDeadPodRightAway(t *testing.T) {
	opts, fake := logsEnv(t, dbPod("al-c374-big-db-1", "big-db", "database", true))
	fake.execErrs = map[string]error{"al-c374-big-db-1": errors.New("connection reset by peer")}

	out, stop := startLogs(t, context.Background(), opts, "big-db", "-f", "--tail", "1")
	waitForOutput(t, out, "[al-c374-big-db-0] pg line 150\n")
	waitForOutput(t, out, "al-c374-big-db-1: connection reset by peer") // before Ctrl+C
	if err := stop(); err == nil {
		t.Fatal("a pod whose stream failed should make the command fail")
	}
}

func TestLogsExecForbiddenSuggestsDbdaemon(t *testing.T) {
	opts, fake := logsEnv(t)
	fake.execErrs = map[string]error{"al-c374-big-db-0": apierrors.NewForbidden(
		schema.GroupResource{Resource: "pods/exec"}, "al-c374-big-db-0", errors.New(`user "dev" cannot create resource "pods/exec"`))}

	_, err := runCmd(t, newLogsCommand(opts), "big-db")
	if err == nil || !strings.Contains(err.Error(), "--source dbdaemon") {
		t.Fatalf("a forbidden exec should suggest --source dbdaemon, got %v", err)
	}
}

func TestLogsDbdaemonOptions(t *testing.T) {
	opts, fake := logsEnv(t)
	if _, err := runCmd(t, newLogsCommand(opts), "big-db", "--source", "dbdaemon", "-p", "--tail", "5"); err != nil {
		t.Fatal(err)
	}
	got := fake.logOpts[0]
	if !got.Previous || got.TailLines == nil || *got.TailLines != 5 || got.Follow {
		t.Fatalf("PodLogOptions = %+v, want previous, tail 5, no follow", got)
	}
}

func TestLogsHeadLimitsContainerLogsClientSide(t *testing.T) {
	opts, fake := logsEnv(t)
	fake.stdout["al-c374-big-db-0"] = lines(10, "daemon")

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--source", "dbdaemon", "--head", "3")
	if err != nil {
		t.Fatal(err)
	}
	if out != "daemon line 1\ndaemon line 2\ndaemon line 3\n" {
		t.Fatalf("--head 3 on dbdaemon:\n%q", out)
	}
}

func TestLogsInvalidFlagCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"--head", "5", "--follow"},
		{"--head", "5", "--tail", "5"},
		{"--head", "0"},
		{"--tail", "-2"},
		{"--previous"}, // only meaningful for dbdaemon
	} {
		opts, fake := logsEnv(t)
		if _, err := runCmd(t, newLogsCommand(opts), append([]string{"big-db"}, args...)...); err == nil {
			t.Errorf("%v should fail", args)
		}
		if len(fake.execs)+len(fake.logOpts) != 0 {
			t.Errorf("%v read logs before rejecting the flags", args)
		}
	}
}

func TestLogsMultiplePodsArePrefixed(t *testing.T) {
	opts, _ := logsEnv(t, dbPod("al-c374-big-db-1", "big-db", "database", true))

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--tail", "1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"[al-c374-big-db-0] pg line 150\n", "[al-c374-big-db-1] standby-pg line 2\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestLogsPodFlag(t *testing.T) {
	opts, fake := logsEnv(t, dbPod("al-c374-big-db-1", "big-db", "database", true))

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--pod", "al-c374-big-db-1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "[") || !strings.Contains(out, "standby-pg line 2") || len(fake.execs) != 1 {
		t.Fatalf("--pod should read only that pod, unprefixed:\n%s", out)
	}

	_, err = runCmd(t, newLogsCommand(opts), "big-db", "--pod", "al-9f1e-other-db-0")
	if err == nil || !strings.Contains(err.Error(), "al-c374-big-db-0") {
		t.Fatalf("--pod from another cluster should fail and list this cluster's pods, got %v", err)
	}
}

func TestLogsNoDatabasePods(t *testing.T) {
	cluster := stuckDBCluster()
	opts := fakeClusterOpts([]runtime.Object{cluster})
	old := newPodLogReader
	t.Cleanup(func() { newPodLogReader = old })
	newPodLogReader = func(*k8s.Clients) podLogReader { return &fakePods{} }

	_, err := runCmd(t, newLogsCommand(opts), "big-db")
	if err == nil || !strings.Contains(err.Error(), "adbo cluster describe big-db") {
		t.Fatalf("want a pointer to describe when no pods exist, got %v", err)
	}
}

func TestLogsSkipsPodsWhoseDatabaseIsNotRunning(t *testing.T) {
	opts, fake := logsEnv(t, dbPod("al-c374-big-db-1", "big-db", "database", false))

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--tail", "1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "al-c374-big-db-1") || !strings.Contains(out, "CrashLoopBackOff") {
		t.Fatalf("want a note that the standby's database isn't running:\n%s", out)
	}
	for _, e := range fake.execs {
		if e[0] == "al-c374-big-db-1" {
			t.Fatal("exec'd into a container that isn't running")
		}
	}
}

func TestLogsHintsAtInternalLogWhenNotReady(t *testing.T) {
	cluster := stuckDBCluster() // DBClusterReconciling
	opts := fakeClusterOpts([]runtime.Object{cluster}, dbPod("al-c374-big-db-0", "big-db", "database", true))
	old := newPodLogReader
	t.Cleanup(func() { newPodLogReader = old })
	newPodLogReader = func(*k8s.Clients) podLogReader {
		return &fakePods{files: map[string]map[string]string{"al-c374-big-db-0": {pgLog: "pg\n", internalLog: "internal\n"}}}
	}

	out, err := runCmd(t, newLogsCommand(opts), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--source internal") {
		t.Fatalf("want a hint about the internal log for a cluster that isn't ready:\n%s", out)
	}

	out, _ = runCmd(t, newLogsCommand(opts), "big-db", "--source", "internal")
	if strings.Contains(out, "--source internal") {
		t.Fatalf("no hint needed when already reading the internal log:\n%s", out)
	}
}

func TestLogsWrap(t *testing.T) {
	opts, fake := logsEnv(t)
	long := strings.Repeat("word ", 30) // 150 chars
	fake.files["al-c374-big-db-0"][pgLog] = long + "\n"
	t.Setenv("COLUMNS", "40")

	out, err := runCmd(t, newLogsCommand(opts), "big-db", "--wrap")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if len(l) > 40 {
			t.Fatalf("line longer than 40 columns with --wrap: %q", l)
		}
	}
	if strings.Count(out, "word") != 30 {
		t.Fatalf("--wrap lost text:\n%s", out)
	}

	out, _ = runCmd(t, newLogsCommand(opts), "big-db")
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("without --wrap, lines are printed as-is:\n%s", out)
	}
}

func TestWrapLine(t *testing.T) {
	for _, tc := range []struct {
		line  string
		width int
		want  []string
	}{
		{"short", 20, []string{"short"}},
		{"aaaa bbbb cccc dddd", 10, []string{"aaaa bbbb", "    cccc", "    dddd"}},
		{"abcdefghijklmnop", 10, []string{"abcdefghij", "    klmnop"}},
		{"no width", 0, []string{"no width"}},
	} {
		if got := wrapLine(tc.line, tc.width); !slices.Equal(got, tc.want) {
			t.Errorf("wrapLine(%q, %d) = %q, want %q", tc.line, tc.width, got, tc.want)
		}
	}
}

func TestRootRegistersLogs(t *testing.T) {
	out, err := runCmd(t, NewRootCommand(), "logs", "--help")
	if err != nil || !strings.Contains(out, "--source") {
		t.Fatalf("logs not registered: %v\n%s", err, out)
	}
}

func TestLogsForbiddenPodListSaysWhereToFixRBAC(t *testing.T) {
	opts, _ := logsEnv(t)
	c, _ := opts.Clients()
	c.Typed.(*kubefake.Clientset).PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
	})

	_, err := runCmd(t, newLogsCommand(opts), "big-db")
	if err == nil {
		t.Fatal("a Forbidden pod list should fail")
	}
	for _, want := range []string{"permission denied", `"omni-test"`, `"dev-gke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}
