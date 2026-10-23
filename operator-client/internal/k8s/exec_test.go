package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
)

// When Ctrl+C closes an exec stream, client-go reports the stderr copy
// failure through the context's logger (remotecommand v2.go). adbo already
// returns stream errors, so that report must not reach the terminal.
func TestExecContextSilencesClosedStreamReports(t *testing.T) {
	var logged strings.Builder
	ctx := klog.NewContext(context.Background(), funcr.New(func(prefix, args string) {
		logged.WriteString(args)
	}, funcr.Options{}))
	report := func(ctx context.Context) string {
		logged.Reset()
		err := errors.New("next reader: read tcp: use of closed network connection")
		utilruntime.HandleErrorWithLogger(klog.FromContext(ctx), err, "Copying stderr failed")
		return logged.String()
	}

	if report(ctx) == "" {
		t.Fatal("control: expected the context's logger to receive the report")
	}
	if got := report(execContext(ctx)); got != "" {
		t.Fatalf("exec streams should drop client-go's report, got %q", got)
	}
}
