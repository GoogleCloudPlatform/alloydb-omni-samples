package connect

import (
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// KubeExec runs a command in a pod over SPDY.
// The exec API has no env field, so env is prefixed as `env KEY=VAL command`.
// The API server records that command line. Do not put a password in env or command.
type KubeExec struct {
	REST  *rest.Config
	Typed kubernetes.Interface
}

func (k KubeExec) Exec(ctx context.Context, namespace, pod, container string, command, env []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	if k.REST == nil || k.Typed == nil {
		return fmt.Errorf("connect: kubernetes exec is not configured")
	}
	if namespace == "" || pod == "" || container == "" {
		return fmt.Errorf("connect: namespace, pod, and container are required")
	}
	if len(command) == 0 {
		return fmt.Errorf("connect: command is required")
	}

	argv := command
	if len(env) > 0 {
		argv = make([]string, 0, 1+len(env)+len(command))
		argv = append(argv, "env")
		argv = append(argv, env...)
		argv = append(argv, command...)
	}

	// TTY mode cannot stream stderr separately.
	streamErr := !tty
	req := k.Typed.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    streamErr,
			TTY:       tty,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(k.REST, http.MethodPost, req.URL())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	var errOut io.Writer
	if streamErr {
		errOut = stderr
	}
	// A remote TTY expects raw keystrokes. Without raw mode, Enter stays a
	// carriage return and shows up as ^M instead of submitting the line.
	err = withTerminal(stdin, tty, func(resize remotecommand.TerminalSizeQueue) error {
		return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdin,
			Stdout:            stdout,
			Stderr:            errOut,
			Tty:               tty,
			TerminalSizeQueue: resize,
		})
	})
	if err != nil {
		return fmt.Errorf("psql exited: %w", err)
	}
	return nil
}
