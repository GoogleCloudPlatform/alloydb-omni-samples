// Package connect starts a local psql session through a port-forward.
package connect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Forwarder opens a tunnel and returns the bound local port plus a stop func.
type Forwarder interface {
	Forward(ctx context.Context, namespace, pod string, local, remote int32) (bound int, stop func(), err error)
}

// Runner locates and runs psql. Tests substitute a fake so the suite does not
// need a database or a psql binary.
type Runner interface {
	LookPath(file string) (string, error)
	Run(ctx context.Context, name string, args []string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer) error
}

// ExecRunner uses the real process environment.
type ExecRunner struct{}

func (ExecRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

func (ExecRunner) Run(ctx context.Context, name string, args []string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("psql exited: %w", err)
	}
	return nil
}

// Execer runs a command inside a container with the caller's streams.
// env is separate from command so a password is not part of the command slice.
type Execer interface {
	Exec(ctx context.Context, namespace, pod, container string, command, env []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error
}

// Session is one connect invocation. Password is passed only as PGPASSWORD.
type Session struct {
	Namespace  string
	Pod        string
	Container  string
	LocalPort  int32
	RemotePort int32
	User       string
	Database   string
	SSLMode    string
	Password   string
	CAPath     string
}

// Start forwards the port, then runs psql attached to the given streams.
// Progress lines go to progress (stderr). A canceled context is not an error:
// Ctrl+C is how the user leaves the session.
func (s Session) Start(ctx context.Context, fwd Forwarder, run Runner, progress io.Writer, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fwd == nil || run == nil {
		return fmt.Errorf("connect: forwarder and runner are required")
	}
	if _, err := run.LookPath("psql"); err != nil {
		return fmt.Errorf("psql was not found on PATH. Install a PostgreSQL client and try again")
	}

	bound, stop, err := fwd.Forward(ctx, s.Namespace, s.Pod, s.LocalPort, s.RemotePort)
	if err != nil {
		return err
	}
	if stop != nil {
		defer stop()
	}

	fmt.Fprintf(progress, "pod: %s\n", s.Pod)
	fmt.Fprintf(progress, "forward: 127.0.0.1:%d -> %s:%d\n", bound, s.Pod, s.RemotePort)
	fmt.Fprintf(progress, "starting psql as %s. Quit with \\q\n", s.User)

	args := []string{
		"-h", "127.0.0.1",
		"-p", strconv.Itoa(bound),
		"-U", s.User,
		"-d", s.Database,
	}
	env := []string{
		"PGPASSWORD=" + s.Password,
		"PGSSLMODE=" + s.SSLMode,
	}
	if s.CAPath != "" {
		env = append(env, "PGSSLROOTCERT="+s.CAPath)
	}
	err = run.Run(ctx, "psql", args, env, stdin, stdout, stderr)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Exec runs psql inside the database container over TCP with SSL.
// It does not port-forward and does not require a local psql binary.
//
// kubectl exec cannot set environment variables, and a TTY echoes stdin, so
// the password is written by a non-interactive exec and read back into
// PGPASSWORD. Neither command line contains the password.
func (s Session) Exec(ctx context.Context, ex Execer, progress io.Writer, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ex == nil {
		return fmt.Errorf("connect: execer is required")
	}
	container := s.Container
	if container == "" {
		container = "database"
	}
	port := s.RemotePort
	if port == 0 {
		port = 5432
	}
	passFile, err := passwordFile()
	if err != nil {
		return err
	}

	fmt.Fprintf(progress, "exec: %s/%s\n", s.Pod, container)
	fmt.Fprintf(progress, "starting psql as %s. Quit with \\q\n", s.User)

	err = ex.Exec(ctx, s.Namespace, s.Pod, container, stashPassword(passFile), nil,
		strings.NewReader(s.Password+"\n"), io.Discard, stderr, false)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if err != nil {
		return redactPassword(fmt.Errorf("prepare session: %w", err), s.Password)
	}

	err = ex.Exec(ctx, s.Namespace, s.Pod, container,
		psqlInPod(passFile, s.User, s.Database, port),
		nil, stdin, stdout, stderr, isTerminal(stdin))
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return redactPassword(err, s.Password)
}

// psqlInPod is the interactive exec. The password is read from passFile,
// then the file is removed, before psql replaces the shell.
func psqlInPod(passFile, user, db string, port int32) []string {
	script := `PGPASSWORD="$(cat "$1")"; rm -f "$1"; export PGPASSWORD; ` +
		`exec psql -h 127.0.0.1 -p "$2" -U "$3" -d "$4"`
	return []string{
		"env", "PGSSLMODE=require", "PSQL_HISTORY=/dev/null",
		"sh", "-c", script,
		"sh",
		passFile,
		strconv.Itoa(int(port)),
		user,
		db,
	}
}

// stashPassword is the non-interactive exec. The password arrives on stdin.
func stashPassword(passFile string) []string {
	return []string{"sh", "-c", `umask 077; cat > "$1"`, "sh", passFile}
}

func passwordFile() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("connect: password file: %w", err)
	}
	return "/tmp/.adbo-pw-" + hex.EncodeToString(b[:]), nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func redactPassword(err error, password string) error {
	if err == nil || password == "" || !strings.Contains(err.Error(), password) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), password, "[redacted]"))
}

// WriteCAFile stores PEM bytes in a 0600 temp file. cleanup removes it.
func WriteCAFile(pem []byte) (path string, cleanup func(), err error) {
	if len(pem) == 0 {
		return "", nil, fmt.Errorf("CA certificate is empty")
	}
	f, err := os.CreateTemp("", "adbo-ca-*.crt")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.Remove(f.Name()) }
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := f.Write(pem); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}
