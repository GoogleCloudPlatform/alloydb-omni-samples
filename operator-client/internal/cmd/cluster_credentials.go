package cmd

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

const maskedPassword = "****"

type connectionInfo struct {
	Host     string `json:"host" yaml:"host"`
	Port     int32  `json:"port" yaml:"port"`
	User     string `json:"user" yaml:"user"`
	DBName   string `json:"dbname" yaml:"dbname"`
	Password string `json:"password" yaml:"password"`
	DSN      string `json:"dsn" yaml:"dsn"`
	URI      string `json:"uri" yaml:"uri"`
}

// newClusterCredentialsCommand is `adbo cluster credentials [CLUSTER_NAME]`.
func newClusterCredentialsCommand(opts *GlobalOptions) *cobra.Command {
	var reveal bool
	cmd := &cobra.Command{
		Use:     "credentials [CLUSTER_NAME]",
		Aliases: []string{"connection-info"},
		Short:   "Show the postgres connection string for a cluster",
		Example: "  adbo cluster credentials my-db\n  adbo cluster credentials my-db --reveal-password\n  adbo cluster connection-info my-db -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := prepareCluster(opts, args)
			if err != nil {
				return err
			}
			return runClusterCredentials(cmd, opts, name, reveal)
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal-password", false, "print the admin password after confirmation")
	return cmd
}

func runClusterCredentials(cmd *cobra.Command, opts *GlobalOptions, name string, reveal bool) error {
	ctx := cmd.Context()
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	obj, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}

	host, port := primaryHostPort(obj)
	if host == "" {
		return fmt.Errorf("cluster %q has no primary endpoint yet", name)
	}
	const user, dbname = "postgres", "postgres"
	password := maskedPassword
	if reveal {
		ok, err := opts.Confirm(cmd, fmt.Sprintf("Reveal the admin password for cluster %q?", name))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("password was not revealed")
		}
		raw, err := adminPassword(ctx, c, c.Namespace, name, obj)
		if err != nil {
			return err
		}
		password = string(raw)
	}

	info := connectionInfo{
		Host:     host,
		Port:     port,
		User:     user,
		DBName:   dbname,
		Password: password,
		DSN:      dsn(host, port, user, dbname, password),
		URI: (&url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(user, password),
			Host:   net.JoinHostPort(host, strconv.Itoa(int(port))),
			Path:   "/" + dbname,
		}).String(),
	}
	table := output.Table{
		Headers: []string{"HOST", "PORT", "USER", "DBNAME", "PASSWORD", "DSN", "URI"},
		Rows: [][]string{{
			info.Host,
			strconv.Itoa(int(info.Port)),
			info.User,
			info.DBName,
			info.Password,
			info.DSN,
			info.URI,
		}},
	}
	return output.Print(cmd.OutOrStdout(), opts.Output, table, info)
}

// primaryHostPort reads status.primary.endpoints[].value, then
// status.primary.url, then the deprecated status.primary.endpoint.
// A missing port falls back to spec.primarySpec.port, or 5432.
func primaryHostPort(obj *unstructured.Unstructured) (string, int32) {
	fallback := cluster.DBPort(obj)
	if obj == nil {
		return "", fallback
	}
	items, _, _ := unstructured.NestedSlice(obj.Object, "status", "primary", "endpoints")
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		val, _ := m["value"].(string)
		if host, port, ok := splitAddress(val, fallback); ok {
			return host, port
		}
	}
	for _, path := range [][]string{
		{"status", "primary", "url"},
		{"status", "primary", "endpoint"},
	} {
		s, _, _ := unstructured.NestedString(obj.Object, path...)
		if host, port, ok := splitAddress(s, fallback); ok {
			return host, port
		}
	}
	return "", fallback
}

// splitAddress accepts "host:port", "[ipv6]:port", or a postgres URL.
// A value with no port uses fallback.
func splitAddress(s string, fallback int32) (string, int32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, false
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil && u.Host != "" {
			if host, port, ok := hostPort(u.Host, fallback); ok {
				return host, port, true
			}
			if host := u.Hostname(); host != "" {
				return host, fallback, true
			}
		}
	}
	return hostPort(s, fallback)
}

// quoteDSN escapes a libpq keyword value. Spaces, quotes, and backslashes
// must be wrapped in single quotes, with \ and ' escaped by a backslash.
func quoteDSN(s string) string {
	if s == "" || !strings.ContainsAny(s, " '\\") {
		return s
	}
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '\'' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
	return b.String()
}

func dsn(host string, port int32, user, dbname, password string) string {
	return fmt.Sprintf("host=%s port=%d user=%s dbname=%s password=%s",
		quoteDSN(host), port, quoteDSN(user), quoteDSN(dbname), quoteDSN(password))
}

func hostPort(s string, fallback int32) (string, int32, bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		h := strings.TrimSpace(s)
		if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") && len(h) >= 2 {
			h = h[1 : len(h)-1]
		}
		return h, fallback, h != ""
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return host, fallback, host != ""
	}
	return host, int32(n), true
}
