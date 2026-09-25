package evaluator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/prototext"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
	findpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/server/proto"
)

func newAuditLog(isAnomaly bool) *auditpb.AuditLogEntry {
	entry := &auditpb.AuditLogEntry{}
	entry.SetIsGroundTruthAnomalous(isAnomaly)
	return entry
}

func newAuditLogWithTime(isAnomaly bool, hourOffset int) *auditpb.AuditLogEntry {
	entry := &auditpb.AuditLogEntry{}
	entry.SetIsGroundTruthAnomalous(isAnomaly)
	// 345600000 ms is 1970-01-05 00:00:00 UTC (Monday)
	entry.SetTimestampMs(345600000 + int64(hourOffset)*3600000)
	return entry
}

func newFinding(isAnomaly bool) *findpb.AnomalyFinding {
	finding := &findpb.AnomalyFinding{}
	finding.SetIsAnomalousPredict(isAnomaly)
	return finding
}

func TestCalculateModelPerformance(t *testing.T) {
	tests := []struct {
		name       string
		logEntries []*auditpb.AuditLogEntry
		findings   []*findpb.AnomalyFinding
		want       EvaluationMetrics
		wantErr    bool
	}{
		{
			name:       "perfect_prediction",
			logEntries: []*auditpb.AuditLogEntry{newAuditLog(true), newAuditLog(false)},
			findings:   []*findpb.AnomalyFinding{newFinding(true), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 1.0, Recall: 1.0},
		},
		{
			name:       "mixed_predictions",
			logEntries: []*auditpb.AuditLogEntry{newAuditLog(true), newAuditLog(false), newAuditLog(false), newAuditLog(false)},
			findings:   []*findpb.AnomalyFinding{newFinding(true), newFinding(true), newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 0.75, Precision: 0.5, Recall: 1.0},
		},
		{
			name:       "all_normal_predictions",
			logEntries: []*auditpb.AuditLogEntry{newAuditLog(false), newAuditLog(false)},
			findings:   []*findpb.AnomalyFinding{newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 0.0, Recall: 0.0},
		},
		{
			name:       "empty_files",
			logEntries: nil,
			findings:   nil,
			want:       EvaluationMetrics{Accuracy: 0.0, Precision: 0.0, Recall: 0.0},
		},
		{
			name:       "mismatched_record_counts",
			logEntries: []*auditpb.AuditLogEntry{newAuditLog(true), newAuditLog(false)},
			findings:   []*findpb.AnomalyFinding{newFinding(true)},
			want:       EvaluationMetrics{Accuracy: 0.0, Precision: 0.0, Recall: 0.0},
			wantErr:    true,
		},
		{
			name:       "nil_elements_skipped",
			logEntries: []*auditpb.AuditLogEntry{nil, newAuditLog(true)},
			findings:   []*findpb.AnomalyFinding{nil, newFinding(true)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 1.0, Recall: 1.0},
		},
		{
			name:       "precision_denominator_zero",
			logEntries: []*auditpb.AuditLogEntry{newAuditLog(true), newAuditLog(false)},
			findings:   []*findpb.AnomalyFinding{newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 0.5, Precision: 0.0, Recall: 0.0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			logsPath := filepath.Join(tempDir, "test_logs.binproto")
			findingsPath := filepath.Join(tempDir, "findings.textproto")

			logsFile, err := os.Create(logsPath)
			if err != nil {
				t.Fatalf("failed to create logs file: %v", err)
			}
			logsWriter := bufio.NewWriter(logsFile)
			for _, entry := range test.logEntries {
				if entry == nil {
					entry = &auditpb.AuditLogEntry{}
				}
				if _, err := protodelim.MarshalTo(logsWriter, entry); err != nil {
					t.Fatalf("failed to write log: %v", err)
				}
			}
			logsWriter.Flush()
			logsFile.Close()

			findingsFile, err := os.Create(findingsPath)
			if err != nil {
				t.Fatalf("failed to create findings file: %v", err)
			}
			findingsWriter := bufio.NewWriter(findingsFile)
			for _, finding := range test.findings {
				if finding == nil {
					finding = &findpb.AnomalyFinding{}
				}
				bytes, err := prototext.Marshal(finding)
				if err != nil {
					t.Fatalf("failed to marshal finding: %v", err)
				}
				if _, err := findingsWriter.WriteString(string(bytes) + "\n"); err != nil {
					t.Fatalf("failed to write finding: %v", err)
				}
			}
			findingsWriter.Flush()
			findingsFile.Close()

			ctx := context.Background()
			got, err := CalculateModelPerformance(ctx, logsPath, findingsPath)
			if (err != nil) != test.wantErr {
				t.Errorf("CalculateModelPerformance() error = %v, wantErr = %v", err, test.wantErr)
				return
			}
			if !test.wantErr && got != test.want {
				t.Errorf("CalculateModelPerformance() got %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCalculateBucketModelPerformance(t *testing.T) {
	tests := []struct {
		name       string
		logEntries []*auditpb.AuditLogEntry
		findings   []*findpb.AnomalyFinding
		want       EvaluationMetrics
		wantErr    bool
	}{
		{
			name:       "perfect_prediction",
			logEntries: []*auditpb.AuditLogEntry{newAuditLogWithTime(true, 0), newAuditLogWithTime(false, 1)},
			findings:   []*findpb.AnomalyFinding{newFinding(true), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 1.0, Recall: 1.0},
		},
		{
			name:       "mixed_predictions",
			logEntries: []*auditpb.AuditLogEntry{newAuditLogWithTime(true, 0), newAuditLogWithTime(false, 1), newAuditLogWithTime(false, 2), newAuditLogWithTime(false, 3)},
			findings:   []*findpb.AnomalyFinding{newFinding(true), newFinding(true), newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 0.75, Precision: 0.5, Recall: 1.0},
		},
		{
			name:       "all_normal_predictions",
			logEntries: []*auditpb.AuditLogEntry{newAuditLogWithTime(false, 0), newAuditLogWithTime(false, 1)},
			findings:   []*findpb.AnomalyFinding{newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 0.0, Recall: 0.0},
		},
		{
			name:       "empty_files",
			logEntries: nil,
			findings:   nil,
			want:       EvaluationMetrics{Accuracy: 0.0, Precision: 0.0, Recall: 0.0},
		},
		{
			name:       "mismatched_record_counts",
			logEntries: []*auditpb.AuditLogEntry{newAuditLogWithTime(true, 0), newAuditLogWithTime(false, 1)},
			findings:   []*findpb.AnomalyFinding{newFinding(true)},
			want:       EvaluationMetrics{Accuracy: 0.0, Precision: 0.0, Recall: 0.0},
			wantErr:    true,
		},
		{
			name:       "nil_elements_skipped",
			logEntries: []*auditpb.AuditLogEntry{nil, newAuditLogWithTime(true, 0)},
			findings:   []*findpb.AnomalyFinding{nil, newFinding(true)},
			want:       EvaluationMetrics{Accuracy: 1.0, Precision: 1.0, Recall: 1.0},
		},
		{
			name:       "precision_denominator_zero",
			logEntries: []*auditpb.AuditLogEntry{newAuditLogWithTime(true, 0), newAuditLogWithTime(false, 1)},
			findings:   []*findpb.AnomalyFinding{newFinding(false), newFinding(false)},
			want:       EvaluationMetrics{Accuracy: 0.5, Precision: 0.0, Recall: 0.0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			logsPath := filepath.Join(tempDir, "test_logs.binproto")
			findingsPath := filepath.Join(tempDir, "findings.textproto")

			logsFile, err := os.Create(logsPath)
			if err != nil {
				t.Fatalf("failed to create logs file: %v", err)
			}
			logsWriter := bufio.NewWriter(logsFile)
			for _, entry := range test.logEntries {
				if entry == nil {
					entry = &auditpb.AuditLogEntry{}
				}
				if _, err := protodelim.MarshalTo(logsWriter, entry); err != nil {
					t.Fatalf("failed to write log: %v", err)
				}
			}
			logsWriter.Flush()
			logsFile.Close()

			findingsFile, err := os.Create(findingsPath)
			if err != nil {
				t.Fatalf("failed to create findings file: %v", err)
			}
			findingsWriter := bufio.NewWriter(findingsFile)
			for _, finding := range test.findings {
				if finding == nil {
					finding = &findpb.AnomalyFinding{}
				}
				bytes, err := prototext.Marshal(finding)
				if err != nil {
					t.Fatalf("failed to marshal finding: %v", err)
				}
				if _, err := findingsWriter.WriteString(string(bytes) + "\n"); err != nil {
					t.Fatalf("failed to write finding: %v", err)
				}
			}
			findingsWriter.Flush()
			findingsFile.Close()

			ctx := context.Background()
			got, err := CalculateBucketModelPerformance(ctx, logsPath, findingsPath, 1*time.Hour)
			if (err != nil) != test.wantErr {
				t.Errorf("CalculateBucketModelPerformance() error = %v, wantErr = %v", err, test.wantErr)
				return
			}
			if !test.wantErr && got != test.want {
				t.Errorf("CalculateBucketModelPerformance() got %+v, want %+v", got, test.want)
			}
		})
	}
}
