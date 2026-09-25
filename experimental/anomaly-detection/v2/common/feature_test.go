package common

import (
	"errors"
	"testing"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

func TestExtract(t *testing.T) {
	tests := []struct {
		name     string
		logEntry *auditpb.AuditLogEntry
		want     ExtractedLogFeatures
		wantErr  error
	}{
		{
			// Happy path: Log entry contains a normalized query template, user, and timestamp.
			name: "valid_query_template",
			logEntry: func() *auditpb.AuditLogEntry {
				entry := &auditpb.AuditLogEntry{}
				entry.SetDbUser("db_user")
				entry.SetNormalizedQueryTemplate("SELECT * FROM users WHERE id = %v")
				entry.SetTimestampMs(1782000000000)
				return entry
			}(),
			want: ExtractedLogFeatures{
				DbUser:        "db_user",
				QueryTemplate: "SELECT * FROM users WHERE id = %v",
				TimestampMs:   1782000000000,
			},
			wantErr: nil,
		},
		{
			// Edge case: Log entry contains an empty normalized query template and zeroed values.
			name: "empty_query_template",
			logEntry: func() *auditpb.AuditLogEntry {
				entry := &auditpb.AuditLogEntry{}
				entry.SetNormalizedQueryTemplate("")
				return entry
			}(),
			want: ExtractedLogFeatures{
				DbUser:        "",
				QueryTemplate: "",
				TimestampMs:   0,
			},
			wantErr: nil,
		},
		{
			// Edge case: The log entry pointer itself is nil.
			name:     "nil_log_entry",
			logEntry: nil,
			wantErr:  ErrInvalidArgument,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Extract(test.logEntry)

			if test.wantErr != nil {
				if err == nil {
					t.Errorf("Extract() got nil error, want error %v", test.wantErr)
				} else if !errors.Is(err, test.wantErr) {
					t.Errorf("Extract() got error %v, want error %v", err, test.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Extract() got unexpected error: %v", err)
			}

			if got.DbUser != test.want.DbUser || got.QueryTemplate != test.want.QueryTemplate || got.TimestampMs != test.want.TimestampMs {
				t.Errorf("Extract(%v) = %v, want %v", test.logEntry, got, test.want)
			}
		})
	}
}
