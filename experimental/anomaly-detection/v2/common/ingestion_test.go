package common

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protodelim"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// writeProtoFile is a test helper that serializes AuditLogEntry protos to a file using protodelim.
func writeProtoFile(ctx context.Context, t *testing.T, path string, entries []*auditpb.AuditLogEntry) {
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create file %s: %v", path, err)
	}
	defer file.Close()

	for _, entry := range entries {
		if _, err := protodelim.MarshalTo(file, entry); err != nil {
			t.Fatalf("failed to marshal entry to %s: %v", path, err)
		}
	}
}

func TestIngestBinProtoStream(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	// 1. Setup empty file
	emptyFile := filepath.Join(tmpDir, "empty.binproto")
	if err := os.WriteFile(emptyFile, nil, 0644); err != nil {
		t.Fatalf("failed to create empty file: %v", err)
	}

	// 2. Setup single entry file
	singleFile := filepath.Join(tmpDir, "single.binproto")
	e1 := &auditpb.AuditLogEntry{}
	e1.SetNormalizedQueryTemplate("SELECT * FROM users WHERE age > %v")
	e1.SetQueryStatement("SELECT * FROM users WHERE age > 20")
	e1.SetDbUser("db_user")
	e1.SetDbName("db_name")
	e1.SetIsGroundTruthAnomalous(false)
	writeProtoFile(ctx, t, singleFile, []*auditpb.AuditLogEntry{e1})

	// 3. Setup multiple entries file
	multipleFile := filepath.Join(tmpDir, "multiple.binproto")
	e2 := &auditpb.AuditLogEntry{}
	e2.SetNormalizedQueryTemplate("INSERT INTO logs VALUES (%v)")
	e2.SetQueryStatement("INSERT INTO logs VALUES ('hello')")
	e2.SetDbUser("db_user")
	e2.SetDbName("db_name")
	e2.SetIsGroundTruthAnomalous(true)
	writeProtoFile(ctx, t, multipleFile, []*auditpb.AuditLogEntry{e1, e2})

	// 4. Setup corrupted stream file
	corruptFile := filepath.Join(tmpDir, "corrupt.binproto")
	if err := os.WriteFile(corruptFile, []byte{0x0A, 0xFF}, 0644); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	customErr := errors.New("custom handler error")

	tests := []struct {
		name      string
		path      string
		handler   func(got *[]*auditpb.AuditLogEntry) LogHandler
		expectErr bool
		wantErr   error
		wantCount int
	}{
		{
			// Edge case: handler is nil.
			name:      "nil_handler",
			path:      singleFile,
			handler:   func(got *[]*auditpb.AuditLogEntry) LogHandler { return nil },
			expectErr: true,
			wantErr:   ErrInvalidArgument,
			wantCount: 0,
		},
		{
			// Edge case: File does not exist under the given path.
			name: "file_not_found",
			path: filepath.Join(tmpDir, "non_existent.binproto"),
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error { return nil }
			},
			expectErr: true,
			wantErr:   ErrNotFound,
			wantCount: 0,
		},
		{
			// Edge case: File is completely empty (0 bytes).
			name: "empty_file",
			path: emptyFile,
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error {
					*got = append(*got, entry)
					return nil
				}
			},
			expectErr: false,
			wantCount: 0,
		},
		{
			// Happy path: File containing exactly one valid entry.
			name: "single_entry",
			path: singleFile,
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error {
					*got = append(*got, entry)
					return nil
				}
			},
			expectErr: false,
			wantCount: 1,
		},
		{
			// Happy path: File containing multiple valid entries.
			name: "multiple_entries",
			path: multipleFile,
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error {
					*got = append(*got, entry)
					return nil
				}
			},
			expectErr: false,
			wantCount: 2,
		},
		{
			// Edge case: The binary stream contains corrupted bytes or truncated files.
			name: "corrupted_stream",
			path: corruptFile,
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error { return nil }
			},
			expectErr: true,
			wantErr:   ErrInvalidArgument,
			wantCount: 0,
		},
		{
			// Edge case: The callback handler returns an error, halting ingestion immediately.
			name: "handler_error_propagation",
			path: multipleFile,
			handler: func(got *[]*auditpb.AuditLogEntry) LogHandler {
				return func(entry *auditpb.AuditLogEntry) error {
					*got = append(*got, entry)
					return customErr
				}
			},
			expectErr: true,
			wantErr:   customErr,
			wantCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []*auditpb.AuditLogEntry
			var testHandler LogHandler
			if test.name == "nil_handler" {
				testHandler = nil
			} else {
				testHandler = test.handler(&got)
			}

			err := IngestBinProtoStream(ctx, test.path, testHandler)

			if test.expectErr {
				if err == nil {
					t.Errorf("IngestBinProtoStream() got nil error, want error")
				} else if test.wantErr != nil && !errors.Is(err, test.wantErr) {
					t.Errorf("IngestBinProtoStream() got error: %v, want error: %v", err, test.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("IngestBinProtoStream() got error: %v, want nil", err)
				}
			}

			if len(got) != test.wantCount {
				t.Errorf("IngestBinProtoStream() processed %d entries, want %d", len(got), test.wantCount)
			}
		})
	}
}
