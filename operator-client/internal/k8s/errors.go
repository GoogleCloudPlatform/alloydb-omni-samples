package k8s

import (
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// formattedError marks a StatusError that Format already translated.
// main and command sites can both call Format without doubling the message.
type formattedError struct {
	msg string
	err error
}

func (e *formattedError) Error() string { return e.msg }
func (e *formattedError) Unwrap() error { return e.err }

func wrap(msg string, err error) error {
	return &formattedError{msg: msg, err: err}
}

// Format turns a Kubernetes API error into an actionable message.
// ns and kubeContext are included so "not found" says where we looked.
// Non-API errors are returned unchanged so command-specific wrapping still works.
func Format(err error, ns, kubeContext string) error {
	if err == nil {
		return nil
	}
	var already *formattedError
	if errors.As(err, &already) {
		return err
	}
	where := location(ns, kubeContext)
	switch {
	case apierrors.IsNotFound(err):
		return wrap(fmt.Sprintf("not found%s: check the name, -n, and --context: %s", where, err), err)
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return wrap(fmt.Sprintf("permission denied%s: check RBAC for this user and resource: %s", where, err), err)
	case apierrors.IsAlreadyExists(err):
		return wrap(fmt.Sprintf("already exists%s: %s", where, err), err)
	case apierrors.IsConflict(err):
		return wrap(fmt.Sprintf("the object changed while we were updating it; retry: %s", err), err)
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsServiceUnavailable(err):
		return wrap(fmt.Sprintf("API server timed out or is unavailable: %s", err), err)
	case apierrors.IsTooManyRequests(err):
		return wrap(fmt.Sprintf("API server rate-limited the request; retry in a moment: %s", err), err)
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "failed calling webhook"):
		return wrap(fmt.Sprintf("operator webhook is not ready (is cert-manager running?): %s", err), err)
	case strings.Contains(msg, "the server could not find the requested resource"):
		return wrap(fmt.Sprintf("AlloyDB Omni CRDs are not installed on this cluster%s: %s", where, err), err)
	default:
		return err
	}
}

// FormatErr is Format using the client's namespace and context.
func (c *Clients) FormatErr(err error) error {
	if c == nil {
		return Format(err, "", "")
	}
	return Format(err, c.Namespace, c.Context)
}

func location(ns, kubeContext string) string {
	switch {
	case ns != "" && kubeContext != "":
		return fmt.Sprintf(" in namespace %q (context %q)", ns, kubeContext)
	case ns != "":
		return fmt.Sprintf(" in namespace %q", ns)
	case kubeContext != "":
		return fmt.Sprintf(" (context %q)", kubeContext)
	default:
		return ""
	}
}
