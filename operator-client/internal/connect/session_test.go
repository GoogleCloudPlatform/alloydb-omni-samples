package connect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

type fakeForward struct {
	called bool
}

func (f *fakeForward) Forward(ctx context.Context, namespace, pod string, local, remote int32) (int, func(), error) {
	f.called = true
	if namespace != "ns" || pod != "db-0" || remote != 5432 {
		return 0, nil, errors.New("unexpected forward target")
	}
	return 54321, func() {}, nil
}

type recordingRunner struct {
	args    []string
	env     []string
	pathErr error
}

func (r *recordingRunner) LookPath(string) (string, error) {
	if r.pathErr != nil {
		return "", r.pathErr
	}
	return "psql", nil
}

func (r *recordingRunner) Run(ctx context.Context, name string, args []string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	r.args = args
	r.env = extraEnv
	return nil
}

type stubExec struct {
	command []string
	env     []string
	err     error
}

func (s *stubExec) Exec(ctx context.Context, namespace, pod, container string, command, env []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	s.command = command
	s.env = env
	return s.err
}

func TestSessionExecDoesNotLeakPassword(t *testing.T) {
	ex := &stubExec{}
	fwd := &fakeForward{}
	var progress bytes.Buffer
	err := Session{
		Namespace: "ns",
		Pod:       "db-0",
		Container: "database",
		User:      "postgres",
		Database:  "postgres",
		Password:  "s3cret-password",
	}.Exec(context.Background(), ex, &progress, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("exec path forwarded a port")
	}
	if strings.Contains(strings.Join(ex.command, " "), "s3cret-password") {
		t.Fatalf("password in command: %v", ex.command)
	}
	if !strings.Contains(strings.Join(ex.env, "\n"), "PGPASSWORD=s3cret-password") {
		t.Fatalf("env %v", ex.env)
	}
	if !strings.Contains(progress.String(), "exec: db-0/database") {
		t.Fatalf("progress %s", progress.String())
	}
}

func TestSessionExecRedactsPassword(t *testing.T) {
	ex := &stubExec{err: errors.New("psql exited: PGPASSWORD=s3cret-password rejected")}
	err := Session{
		Pod:      "db-0",
		User:     "postgres",
		Database: "postgres",
		Password: "s3cret-password",
	}.Exec(context.Background(), ex, &bytes.Buffer{}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "s3cret-password") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("err %v", err)
	}
}

func TestWriteCAFileMode(t *testing.T) {
	path, cleanup, err := WriteCAFile([]byte("-----BEGIN CERT-----\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestSessionDoesNotLeakPassword(t *testing.T) {
	r := &recordingRunner{}
	fwd := &fakeForward{}
	var progress bytes.Buffer
	err := Session{
		Namespace:  "ns",
		Pod:        "db-0",
		RemotePort: 5432,
		User:       "postgres",
		Database:   "postgres",
		SSLMode:    "verify-ca",
		Password:   "s3cret-password",
		CAPath:     "/tmp/ca.crt",
	}.Start(context.Background(), fwd, r, &progress, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.called {
		t.Fatal("forward was not called")
	}
	if strings.Contains(strings.Join(r.args, " "), "s3cret-password") {
		t.Fatalf("password in args: %v", r.args)
	}
	if !strings.Contains(strings.Join(r.env, "\n"), "PGPASSWORD=s3cret-password") {
		t.Fatal("expected PGPASSWORD in the child environment")
	}
	if !strings.Contains(progress.String(), "127.0.0.1:54321") || !strings.Contains(progress.String(), `Quit with \q`) {
		t.Fatalf("progress: %s", progress.String())
	}
}

func TestSessionMissingPsql(t *testing.T) {
	r := &recordingRunner{pathErr: errors.New("not found")}
	err := Session{Password: "s3cret-password"}.Start(context.Background(), &fakeForward{}, r, &bytes.Buffer{}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "s3cret-password") || !strings.Contains(err.Error(), "psql") {
		t.Fatalf("err %v", err)
	}
}
