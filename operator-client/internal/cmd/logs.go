package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

const (
	logSourcePostgres = "postgres"
	logSourceInternal = "internal"
	logSourceDBDaemon = "dbdaemon"
)

// logFiles are the diagnostic logs the database container writes under /obs.
var logFiles = map[string]string{
	logSourcePostgres: "/obs/diagnostic/postgresql.log",
	logSourceInternal: "/obs/diagnostic/postgresql.internal",
}

// podLogReader reads logs from a database pod.
type podLogReader interface {
	// Exec runs command in the container, streaming its stdout and stderr.
	Exec(ctx context.Context, pod, container string, command []string, stdout, stderr io.Writer) error
	ContainerLogs(ctx context.Context, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

// newPodLogReader talks to the API server. Tests replace it because the
// fake clientset can't exec into pods.
var newPodLogReader = func(c *k8s.Clients) podLogReader { return clientLogReader{c} }

type clientLogReader struct{ c *k8s.Clients }

func (r clientLogReader) Exec(ctx context.Context, pod, container string, command []string, stdout, stderr io.Writer) error {
	return r.c.Exec(ctx, r.c.Namespace, pod, container, command, stdout, stderr)
}

func (r clientLogReader) ContainerLogs(ctx context.Context, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	return r.c.Typed.CoreV1().Pods(r.c.Namespace).GetLogs(pod, opts).Stream(ctx)
}

type logsOptions struct {
	source   string
	tail     int64
	head     int64
	follow   bool
	wrap     bool
	pod      string
	previous bool
}

func newLogsCommand(opts *GlobalOptions) *cobra.Command {
	lo := &logsOptions{}

	cmd := &cobra.Command{
		Use:   "logs [CLUSTER_NAME]",
		Short: "Show a cluster's database logs",
		Long: `Show logs from a cluster's database pods (the primary and any standbys).
With more than one pod, each line starts with its pod name.

Sources (--source):
  postgres   PostgreSQL server log, /obs/diagnostic/postgresql.log (default)
  internal   AlloyDB internal log, /obs/diagnostic/postgresql.internal.
             When the database won't start, the cause is usually here.
  dbdaemon   output of the database container itself (dbdaemon)`,
		Example: "  adbo logs my-db\n" +
			"  adbo logs my-db --source internal --tail 50\n" +
			"  adbo logs my-db -f\n" +
			"  adbo logs my-db --head 20 --wrap\n" +
			"  adbo logs my-db --source dbdaemon --previous",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd, opts, lo, args)
		},
	}

	f := cmd.Flags()
	f.StringVar(&lo.source, "source", logSourcePostgres, "log to show: postgres, internal, or dbdaemon")
	f.Int64VarP(&lo.tail, "tail", "t", 100, "show the last N lines; -1 shows everything")
	// No -h shorthand: cobra uses -h for --help on every command.
	f.Int64Var(&lo.head, "head", 0, "show the first N lines instead of the last")
	f.BoolVarP(&lo.follow, "follow", "f", false, "keep streaming new lines")
	f.BoolVarP(&lo.wrap, "wrap", "w", false, "wrap long lines to the terminal width")
	f.StringVar(&lo.pod, "pod", "", "read only this database pod")
	f.BoolVarP(&lo.previous, "previous", "p", false, "with --source dbdaemon, show the previous (crashed) container's output")
	return cmd
}

func (lo *logsOptions) validate(flags *pflag.FlagSet) error {
	if _, isFile := logFiles[lo.source]; !isFile && lo.source != logSourceDBDaemon {
		return fmt.Errorf("invalid --source %q: use postgres, internal, or dbdaemon", lo.source)
	}
	if flags.Changed("head") {
		switch {
		case lo.head <= 0:
			return errors.New("--head must be greater than 0")
		case lo.follow:
			return errors.New("--head can't be combined with --follow")
		case flags.Changed("tail"):
			return errors.New("use --head or --tail, not both")
		}
	}
	if lo.tail < -1 {
		return errors.New("--tail must be -1 (everything) or 0 or more")
	}
	if lo.previous && lo.source != logSourceDBDaemon {
		return errors.New("--previous only works with --source dbdaemon; the log files survive restarts, so read them normally")
	}
	return nil
}

func runLogs(cmd *cobra.Command, opts *GlobalOptions, lo *logsOptions, args []string) error {
	if err := lo.validate(cmd.Flags()); err != nil {
		return err
	}
	name, err := opts.ClusterName(args)
	if err != nil {
		return err
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	errOut := cmd.ErrOrStderr()

	obj, err := getDBCluster(ctx, c, name)
	if err != nil {
		return err
	}
	pods, err := databasePods(ctx, c, name)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("dbcluster %q has no database pods; run `adbo cluster describe %s` to see why", name, name)
	}
	if lo.pod != "" {
		if pods, err = pickPod(pods, lo.pod, name); err != nil {
			return err
		}
	}

	if phase := nestedString(obj.Object, "status", "phase"); lo.source == logSourcePostgres && phase != "" && phase != "DBClusterReady" {
		fmt.Fprintf(errOut, "note: dbcluster %q is %s. If the database won't start, the cause is usually in the internal log: adbo logs %s --source internal\n", name, phase, name)
	}

	// Log files are read by exec, which needs a running database container.
	var readable []corev1.Pod
	for _, p := range pods {
		if reason := databaseNotRunning(p); reason != "" && lo.source != logSourceDBDaemon {
			fmt.Fprintf(errOut, "skipping pod %s: its database container is not running (%s)\n", p.Name, reason)
			continue
		}
		readable = append(readable, p)
	}
	if len(readable) == 0 {
		return fmt.Errorf("no running database container in dbcluster %q; try `adbo logs %s --source dbdaemon --previous` or `adbo cluster describe %s`", name, name, name)
	}

	reader := newPodLogReader(c)
	stdout := &lockedWriter{w: cmd.OutOrStdout()}
	stderr := &lockedWriter{w: errOut}
	width := 0
	if lo.wrap {
		width = terminalWidth(cmd.OutOrStdout())
	}
	prefix := func(pod string) string {
		if len(readable) > 1 {
			return "[" + pod + "] "
		}
		return ""
	}
	stream := func(pod string) error {
		err := streamPodLogs(ctx, reader, lo, pod,
			&lineWriter{out: stdout, prefix: prefix(pod), width: width},
			&lineWriter{out: stderr, prefix: prefix(pod)})
		if err != nil && lo.source != logSourceDBDaemon && isForbidden(err) {
			err = fmt.Errorf("%w\nyour account may not be allowed to exec into pods; `adbo logs %s --source dbdaemon` only needs pods/log", err, name)
		}
		return err
	}

	if !lo.follow {
		var errs []error
		for _, p := range readable {
			if err := stream(p.Name); err != nil {
				errs = append(errs, fmt.Errorf("pod %s: %w", p.Name, err))
			}
		}
		return errors.Join(errs...)
	}

	// Follow every pod at once until Ctrl+C. A stream that dies is reported
	// right away, while the others keep going.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
	)
	for _, p := range readable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := stream(p.Name); err != nil && ctx.Err() == nil {
				stderr.writeString(fmt.Sprintf("error: pod %s: %v\n", p.Name, err))
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failed > 0 {
		return fmt.Errorf("%d of %d log streams failed (see above)", failed, len(readable))
	}
	return nil
}

func isForbidden(err error) bool {
	// Exec upgrade failures aren't always StatusErrors.
	return apierrors.IsForbidden(err) || strings.Contains(strings.ToLower(err.Error()), "forbidden")
}

func databasePods(ctx context.Context, c *k8s.Clients, cluster string) ([]corev1.Pod, error) {
	list, err := c.Typed.CoreV1().Pods(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: k8s.DatabasePodSelector(cluster),
	})
	if err != nil {
		return nil, c.FormatErr(fmt.Errorf("listing database pods of dbcluster %q: %w", cluster, err))
	}
	pods := list.Items
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func pickPod(pods []corev1.Pod, want, cluster string) ([]corev1.Pod, error) {
	var names []string
	for _, p := range pods {
		if p.Name == want {
			return []corev1.Pod{p}, nil
		}
		names = append(names, p.Name)
	}
	return nil, fmt.Errorf("pod %q is not a database pod of dbcluster %q; its database pods are: %s", want, cluster, strings.Join(names, ", "))
}

// databaseNotRunning returns why the database container isn't running, or "".
func databaseNotRunning(p corev1.Pod) string {
	for _, s := range p.Status.ContainerStatuses {
		if s.Name != k8s.DatabaseContainer {
			continue
		}
		switch {
		case s.State.Running != nil:
			return ""
		case s.State.Waiting != nil:
			return s.State.Waiting.Reason
		case s.State.Terminated != nil:
			return "terminated: " + s.State.Terminated.Reason
		}
	}
	return "pod is " + string(p.Status.Phase)
}

func streamPodLogs(ctx context.Context, r podLogReader, lo *logsOptions, pod string, w, errW *lineWriter) error {
	if lo.source != logSourceDBDaemon {
		// stderr is shown as it arrives, so `tail -F` complaints such as
		// "cannot open" don't leave follow hanging silently.
		err := r.Exec(ctx, pod, k8s.DatabaseContainer, logFileCommand(lo, logFiles[lo.source]), w, errW)
		errW.Flush()
		if flushErr := w.Flush(); err == nil {
			err = flushErr
		}
		return err
	}

	po := &corev1.PodLogOptions{Container: k8s.DatabaseContainer, Follow: lo.follow, Previous: lo.previous}
	if lo.head == 0 && lo.tail >= 0 {
		tail := lo.tail
		po.TailLines = &tail
	}
	rc, err := r.ContainerLogs(ctx, pod, po)
	if err != nil {
		return err
	}
	defer rc.Close()
	if lo.head > 0 {
		err = copyLines(w, rc, lo.head)
	} else {
		_, err = io.Copy(w, rc)
	}
	if flushErr := w.Flush(); err == nil {
		err = flushErr
	}
	return err
}

// logFileCommand reads a log file inside the container with coreutils,
// so only the requested lines cross the network.
func logFileCommand(lo *logsOptions, path string) []string {
	n := strconv.FormatInt(lo.tail, 10)
	switch {
	case lo.head > 0:
		return []string{"head", "-n", strconv.FormatInt(lo.head, 10), path}
	case lo.follow:
		if lo.tail < 0 {
			n = "+1" // from the first line
		}
		// -F keeps following across log rotation.
		return []string{"tail", "-n", n, "-F", path}
	case lo.tail < 0:
		return []string{"cat", path}
	}
	return []string{"tail", "-n", n, path}
}

// copyLines copies the first n lines of r. The container log API has no
// head option, so the rest is dropped client-side.
func copyLines(w io.Writer, r io.Reader, n int64) error {
	br := bufio.NewReader(r)
	for i := int64(0); i < n; i++ {
		line, err := br.ReadString('\n')
		if line != "" {
			if _, werr := io.WriteString(w, line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lockedWriter keeps lines from pods followed in parallel from interleaving.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) writeString(s string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := io.WriteString(l.w, s)
	return err
}

// lineWriter writes whole lines with an optional pod prefix and wrapping.
type lineWriter struct {
	out    *lockedWriter
	prefix string
	width  int // 0 = don't wrap
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if err := l.emit(string(l.buf[:i])); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
}

// Flush writes a final line that had no trailing newline.
func (l *lineWriter) Flush() error {
	if len(l.buf) == 0 {
		return nil
	}
	err := l.emit(string(l.buf))
	l.buf = nil
	return err
}

func (l *lineWriter) emit(line string) error {
	parts := []string{line}
	if l.width > 0 {
		parts = wrapLine(line, l.width-len(l.prefix))
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(l.prefix)
		b.WriteString(p)
		b.WriteByte('\n')
	}
	return l.out.writeString(b.String())
}

const wrapIndent = "    "

// wrapLine splits line into pieces of at most width columns, breaking at
// spaces when it can. Continuation pieces are indented so they don't look
// like new log lines. A width too small to be useful disables wrapping.
func wrapLine(line string, width int) []string {
	if width <= len(wrapIndent)+1 {
		return []string{line}
	}
	r := []rune(line)
	var out []string
	limit := width
	for len(r) > limit {
		cut := limit
		if sp := lastSpace(r[:limit+1]); sp > 0 {
			cut = sp
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = trimLeftSpaces(r[cut:])
		limit = width - len(wrapIndent)
	}
	out = append(out, string(r))
	for i := 1; i < len(out); i++ {
		out[i] = wrapIndent + out[i]
	}
	return out
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == ' ' {
			return i
		}
	}
	return -1
}

func trimLeftSpaces(r []rune) []rune {
	for len(r) > 0 && r[0] == ' ' {
		r = r[1:]
	}
	return r
}

// terminalWidth is the width of out if it's a terminal, else $COLUMNS,
// else 120.
func terminalWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 120
}
