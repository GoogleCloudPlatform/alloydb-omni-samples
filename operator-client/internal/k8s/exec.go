package k8s

import (
	"context"
	"io"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
)

// Exec runs command in a container and streams its output, like
// `kubectl exec` without a TTY. It uses WebSockets and falls back to SPDY
// for API servers that don't support them, the same way kubectl does.
// A nil stdout or stderr is not requested from the server.
func (c *Clients) Exec(ctx context.Context, namespace, pod, container string, command []string, stdout, stderr io.Writer) error {
	req := c.Typed.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
		}, scheme.ParameterCodec)

	spdy, err := remotecommand.NewSPDYExecutor(c.REST, "POST", req.URL())
	if err != nil {
		return err
	}
	ws, err := remotecommand.NewWebSocketExecutor(c.REST, "GET", req.URL().String())
	if err != nil {
		return err
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	return exec.StreamWithContext(execContext(ctx), remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
}

// execContext gives the stream a logger that drops everything. When Ctrl+C
// closes a stream, client-go logs "Copying stderr failed: use of closed
// network connection" to stderr; errors that matter are returned anyway.
func execContext(ctx context.Context) context.Context {
	return klog.NewContext(ctx, logr.Discard())
}
