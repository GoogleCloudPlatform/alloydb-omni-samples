package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"

	"google.golang.org/protobuf/encoding/protodelim"

	genpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

// errorWriter simulates an I/O failure (e.g., disk full).
type errorWriter struct{}

func (w *errorWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("simulated write error")
}

func TestNewLogGenerator(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	g := NewLogGenerator(rng)
	if g == nil {
		t.Fatal("NewLogGenerator returned nil")
	}
	if g.rng != rng {
		t.Errorf("NewLogGenerator did not initialize rng correctly")
	}
}

func TestGenerateLog(t *testing.T) {
	tests := []struct {
		name         string
		configText   string
		customWriter io.Writer // nil means use a fresh *bytes.Buffer
		wantErr      bool
		wantQuery    string
		wantAnomaly  bool
	}{
		{
			name: "successful_generation",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
probability_weight: 1.0
`,
			wantQuery: "SELECT 1",
		},
		{
			name: "writer_failure",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
probability_weight: 1.0
`,
			customWriter: &errorWriter{},
			wantErr:      true,
		},
		{
			name: "multiple_placeholders",
			configText: `
template: {
	normalized_query_template: "UPDATE items SET price = %v WHERE id = %v"
	placeholder_values: { int_placeholder: { min: 10 max: 99 } }
	placeholder_values: { int_placeholder: { min: 1000 max: 9999 } }
}
probability_weight: 1.0
`,
			wantQuery: "UPDATE items SET price = 45 WHERE id = 3987",
		},
		{
			name: "no_placeholders",
			configText: `
template: {
	normalized_query_template: "SELECT * FROM static_table"
	is_ground_truth_anomalous: true
}
probability_weight: 1.0
`,
			wantQuery:   "SELECT * FROM static_table",
			wantAnomaly: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := parseProto(t, test.configText, &genpb.WeightedQueryTemplateConfig{})
			generator := NewLogGenerator(rand.New(rand.NewSource(42)))

			w := test.customWriter
			if w == nil {
				w = &bytes.Buffer{}
			}

			err := generator.GenerateLog(cfg, w)
			if (err != nil) != test.wantErr {
				t.Errorf("GenerateLog() error = %v, wantErr %v", err, test.wantErr)
			}

			if !test.wantErr {
				buf, ok := w.(*bytes.Buffer)
				if !ok {
					t.Fatalf("expected *bytes.Buffer writer when wantErr is false")
				}
				reader := bufio.NewReader(buf)
				entry := &genpb.AuditLogEntry{}
				if err := protodelim.UnmarshalFrom(reader, entry); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
				if got, want := entry.GetQueryStatement(), test.wantQuery; got != want {
					t.Errorf("query_statement = %q, want %q", got, want)
				}
				if got, want := entry.GetIsGroundTruthAnomalous(), test.wantAnomaly; got != want {
					t.Errorf("is_ground_truth_anomalous = %t, want %t", got, want)
				}
			}
		})
	}
}

func TestBuildBucketLog(t *testing.T) {
	tests := []struct {
		name        string
		configText  string
		isSpike     bool
		timestampMs int64
		wantQuery   string
		wantUser    string
		wantSpike   bool
	}{
		{
			name: "successful_bucket_log_generation",
			configText: `
template: {
	normalized_query_template: "SELECT * FROM orders WHERE id = %v"
	placeholder_values: { int_placeholder: { min: 10 max: 10 } }
}
bucket_config: {
	db_user: "custom_user"
}
`,
			isSpike:     true,
			timestampMs: 123456789000,
			wantQuery:   "SELECT * FROM orders WHERE id = 10",
			wantUser:    "custom_user",
			wantSpike:   true,
		},
		{
			name: "default_user_on_empty_db_user",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	db_user: ""
}
`,
			isSpike:     false,
			timestampMs: 123456789000,
			wantQuery:   "SELECT 1",
			wantUser:    "db_user",
			wantSpike:   false,
		},
		{
			name: "default_user_on_nil_bucket_config",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
`,
			isSpike:     false,
			timestampMs: 123456789000,
			wantQuery:   "SELECT 1",
			wantUser:    "db_user",
			wantSpike:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := parseProto(t, test.configText, &genpb.BucketQueryTemplateConfig{})
			generator := NewLogGenerator(rand.New(rand.NewSource(42)))

			got := generator.BuildBucketLog(cfg, test.isSpike, test.timestampMs)
			if got.GetQueryStatement() != test.wantQuery {
				t.Errorf("QueryStatement = %q, want %q", got.GetQueryStatement(), test.wantQuery)
			}
			if got.GetDbUser() != test.wantUser {
				t.Errorf("DbUser = %q, want %q", got.GetDbUser(), test.wantUser)
			}
			if got.GetIsGroundTruthAnomalous() != test.wantSpike {
				t.Errorf("IsGroundTruthAnomalous = %t, want %t", got.GetIsGroundTruthAnomalous(), test.wantSpike)
			}
			if got.GetTimestampMs() != test.timestampMs {
				t.Errorf("TimestampMs = %d, want %d", got.GetTimestampMs(), test.timestampMs)
			}
		})
	}
}

func TestBuildQueryStatement(t *testing.T) {
	tests := []struct {
		name       string
		configText string
		seed       int64
		wantQuery  string
	}{
		{
			name: "empty_allowed_values",
			configText: `
normalized_query_template: "SELECT * FROM users WHERE name = '%v'"
placeholder_values: { string_placeholder: { } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM users WHERE name = ''",
		},
		{
			name: "string_allowed_values",
			configText: `
normalized_query_template: "SELECT * FROM users WHERE name = '%v'"
placeholder_values: { string_placeholder: { allowed_values: ["alice"] } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM users WHERE name = 'alice'",
		},
		{
			name: "single_value_range",
			configText: `
normalized_query_template: "SELECT * FROM orders WHERE amount = %v AND status = %v"
placeholder_values: { int_placeholder: { min: 42 max: 42 } }
placeholder_values: { float_placeholder: { min: 10.5 max: 10.5 } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM orders WHERE amount = 42 AND status = 10.5",
		},
		{
			name: "negative_val_range",
			configText: `
normalized_query_template: "SELECT * FROM metrics WHERE diff = %v"
placeholder_values: { int_placeholder: { min: -100 max: -10 } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM metrics WHERE diff = -67",
		},
		{
			name: "escaped_percent_signs",
			configText: `
normalized_query_template: "SELECT * FROM users WHERE name LIKE '%%alice%%' AND age = %v"
placeholder_values: { int_placeholder: { min: 18 max: 18 } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM users WHERE name LIKE '%alice%' AND age = 18",
		},
		{
			name: "empty_range_defaults_to_zero",
			configText: `
normalized_query_template: "SELECT * FROM orders WHERE amount = %v AND status = %v"
placeholder_values: { int_placeholder: { } }
placeholder_values: { float_placeholder: { } }
`,
			seed:      42,
			wantQuery: "SELECT * FROM orders WHERE amount = 0 AND status = 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := parseProto(t, test.configText, &genpb.QueryTemplateConfig{})
			generator := NewLogGenerator(rand.New(rand.NewSource(test.seed)))

			got := generator.buildQueryStatement(cfg)
			if got != test.wantQuery {
				t.Errorf("buildQueryStatement() = %q, want %q", got, test.wantQuery)
			}
		})
	}
}
