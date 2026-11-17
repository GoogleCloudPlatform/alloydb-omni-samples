// Package forward opens a local port-forward to a database pod.
package forward

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// Tunnel forwards a local port to a pod port over SPDY.
// Bind address is 127.0.0.1 only, so the database is not exposed on other interfaces.
type Tunnel struct {
	REST *rest.Config
}

// Forward blocks until the listener is ready. local 0 asks the kernel for a free port.
// stop closes the tunnel. It is safe to call more than once.
// The parent ctx cancels the tunnel (first Ctrl+C). stop does the same when psql exits.
func (t Tunnel) Forward(ctx context.Context, namespace, pod string, local, remote int32) (int, func(), error) {
	if t.REST == nil {
		return 0, nil, fmt.Errorf("port-forward: kubernetes REST config is missing")
	}
	if namespace == "" || pod == "" {
		return 0, nil, fmt.Errorf("port-forward: namespace and pod are required")
	}
	if remote < 1 || remote > 65535 {
		return 0, nil, fmt.Errorf("port-forward: remote port %d is invalid", remote)
	}
	if local < 0 || local > 65535 {
		return 0, nil, fmt.Errorf("port-forward: local port %d is invalid", local)
	}

	transport, upgrader, err := spdy.RoundTripperFor(t.REST)
	if err != nil {
		return 0, nil, fmt.Errorf("port-forward: %w", err)
	}
	host, err := restEndpoint(t.REST)
	if err != nil {
		return 0, nil, err
	}
	host.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, pod)
	host.RawQuery = ""
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, host)

	fwdCtx, cancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	pf, err := portforward.NewOnAddressesWithContext(
		fwdCtx,
		dialer,
		[]string{"127.0.0.1"},
		[]string{fmt.Sprintf("%d:%d", local, remote)},
		ready,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		cancel()
		return 0, nil, fmt.Errorf("port-forward: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- pf.ForwardPorts()
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-errCh:
			case <-time.After(2 * time.Second):
			}
		})
	}

	select {
	case <-ready:
		ports, err := pf.GetPorts()
		if err != nil {
			stop()
			return 0, nil, fmt.Errorf("port-forward: %w", err)
		}
		if len(ports) == 0 || ports[0].Local == 0 {
			stop()
			return 0, nil, fmt.Errorf("port-forward: listener did not bind a local port")
		}
		return int(ports[0].Local), stop, nil
	case err := <-errCh:
		cancel()
		if err == nil {
			err = fmt.Errorf("port-forward stopped before it was ready")
		}
		return 0, nil, fmt.Errorf("port-forward to pod %s: %w", pod, err)
	case <-ctx.Done():
		stop()
		return 0, nil, ctx.Err()
	}
}

func restEndpoint(cfg *rest.Config) (*url.URL, error) {
	host := cfg.Host
	if host == "" {
		return nil, fmt.Errorf("port-forward: REST config has no host")
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("port-forward: parse API host: %w", err)
	}
	if u.Host == "" {
		// Host was a bare hostname without a scheme.
		u, err = url.Parse("https://" + host)
		if err != nil {
			return nil, fmt.Errorf("port-forward: parse API host: %w", err)
		}
	}
	return u, nil
}
