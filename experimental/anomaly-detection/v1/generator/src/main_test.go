package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/prototext"

	genpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

// backupFlags saves global flag values and registers a cleanup to restore them.
func backupFlags(t *testing.T) {
	t.Helper()
	origConfigFile := *configFile
	origOutputDir := *outputDir
	origNumLogs := *numLogs
	origStart := *startTimeMs
	origTrainEnd := *trainEndTimeMs
	origEnd := *endTimeMs
	origBucketSize := *bucketSizeMinutes
	t.Cleanup(func() {
		*configFile = origConfigFile
		*outputDir = origOutputDir
		*numLogs = origNumLogs
		*startTimeMs = origStart
		*trainEndTimeMs = origTrainEnd
		*endTimeMs = origEnd
		*bucketSizeMinutes = origBucketSize
	})
}

func decodeLogs(t *testing.T, r io.Reader) []*genpb.AuditLogEntry {
	t.Helper()
	var entries []*genpb.AuditLogEntry
	bufReader := bufio.NewReader(r)
	for {
		entry := &genpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(bufReader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("failed to parse log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestValidateSharedFlags(t *testing.T) {
	backupFlags(t)

	tests := []struct {
		name          string
		configFile    string
		outputDirType string // "temp_dir", "empty_dir", "regular_file"
		wantErr       bool
	}{
		{
			name:          "ValidSharedFlags",
			configFile:    "test.textproto",
			outputDirType: "temp_dir",
			wantErr:       false,
		},
		{
			name:          "ConfigFileEmpty_ReturnsError",
			configFile:    "",
			outputDirType: "temp_dir",
			wantErr:       true,
		},
		{
			name:          "OutputDirEmpty_ReturnsError",
			configFile:    "test.textproto",
			outputDirType: "empty_dir",
			wantErr:       true,
		},
		{
			name:          "OutputDirIsRegularFile_ReturnsError",
			configFile:    "test.textproto",
			outputDirType: "regular_file",
			wantErr:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			*configFile = test.configFile

			var outDir string
			switch test.outputDirType {
			case "empty_dir":
				outDir = ""
			case "regular_file":
				outDir = filepath.Join(t.TempDir(), "file.txt")
				if err := os.WriteFile(outDir, []byte("test"), 0644); err != nil {
					t.Fatalf("Failed to write temporary test file: %v", err)
				}
			default:
				outDir = t.TempDir()
			}
			*outputDir = outDir

			err := validateSharedFlags(context.Background())
			if (err != nil) != test.wantErr {
				t.Errorf("validateSharedFlags() got error = %v, wantErr = %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateScenarioFlags(t *testing.T) {
	backupFlags(t)

	tests := []struct {
		name              string
		scenario          string // "weighted", "bucket"
		configs           *genpb.QueryTemplateConfigs
		numLogs           int
		startTime         int64
		trainEndTime      int64
		endTime           int64
		bucketSizeMinutes int
		wantErr           bool
	}{
		{
			name:     "WeightedSampling",
			scenario: "weighted",
			numLogs:  100,
			wantErr:  false,
		},
		{
			name:     "WeightedSampling_NonPositiveLogs",
			scenario: "weighted",
			numLogs:  0,
			wantErr:  true,
		},
		{
			name:              "BucketSampling",
			scenario:          "bucket",
			startTime:         1000,
			endTime:           2000,
			trainEndTime:      1500,
			bucketSizeMinutes: 60,
			wantErr:           false,
		},
		{
			name:              "BucketSampling_TrainEndTimeZero_ReturnsError",
			scenario:          "bucket",
			startTime:         1000,
			endTime:           2000,
			trainEndTime:      0,
			bucketSizeMinutes: 60,
			wantErr:           true,
		},
		{
			name:              "BucketSampling_NonPositiveBucketSize",
			scenario:          "bucket",
			startTime:         1000,
			endTime:           2000,
			trainEndTime:      1500,
			bucketSizeMinutes: -10,
			wantErr:           true,
		},
		{
			name:              "BucketSampling_NonPositiveStartTime",
			scenario:          "bucket",
			startTime:         0,
			endTime:           2000,
			trainEndTime:      1500,
			bucketSizeMinutes: 60,
			wantErr:           true,
		},
		{
			name:              "BucketSampling_EndTimeBeforeStartTime",
			scenario:          "bucket",
			startTime:         1000,
			endTime:           500,
			bucketSizeMinutes: 60,
			wantErr:           true,
		},
		{
			name:              "BucketSampling_TrainEndTimeOutOfBounds",
			scenario:          "bucket",
			startTime:         1000,
			endTime:           2000,
			trainEndTime:      2500,
			bucketSizeMinutes: 60,
			wantErr:           true,
		},
		{
			name:    "NilConfigs_ReturnsError",
			configs: nil,
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			*numLogs = test.numLogs
			*startTimeMs = test.startTime
			*trainEndTimeMs = test.trainEndTime
			*endTimeMs = test.endTime
			*bucketSizeMinutes = test.bucketSizeMinutes

			var configs *genpb.QueryTemplateConfigs
			if test.configs != nil {
				configs = test.configs
			} else {
				if test.scenario == "bucket" {
					configs = parseProto(t, "bucket_sampling {}", &genpb.QueryTemplateConfigs{})
				} else if test.scenario == "weighted" {
					configs = parseProto(t, "weighted_sampling {}", &genpb.QueryTemplateConfigs{})
				}
			}

			err := validateScenarioFlags(context.Background(), configs)
			if (err != nil) != test.wantErr {
				t.Errorf("validateScenarioFlags() got error = %v, wantErr = %v", err, test.wantErr)
			}
		})
	}
}

func TestGenerateLogs(t *testing.T) {
	tempDir := t.TempDir()
	configText := `
weighted_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		probability_weight: 1.0
	}
}`
	configs := parseProto(t, configText, &genpb.QueryTemplateConfigs{})

	err := GenerateLogs(context.Background(), configs, 1, 42, tempDir)
	if err != nil {
		t.Fatalf("GenerateLogs failed: %v", err)
	}

	// Verify file is created and contains binary logs.
	logPath := filepath.Join(tempDir, "logs.binproto")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("logs.binproto file not created: %v", err)
	}
}

func TestGenerateLogsWeighted(t *testing.T) {
	text := `
weighted_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT * FROM users WHERE age > %v"
			placeholder_values: { int_placeholder: { min: 18 max: 65 } }
			is_ground_truth_anomalous: false
		}
		probability_weight: 0.70
	}
	query_template_configs: {
		template: {
			normalized_query_template: "INSERT INTO logs VALUES (%v)"
			placeholder_values: { float_placeholder: { min: 100.0 max: 200.0 } }
			is_ground_truth_anomalous: true
		}
		probability_weight: 0.30
	}
}`
	configs := parseProto(t, text, &genpb.QueryTemplateConfigs{})

	var buf bytes.Buffer
	rng := rand.New(rand.NewSource(42))
	if err := GenerateLogsWeighted(configs, 20, rng, &buf); err != nil {
		t.Fatalf("GenerateLogsWeighted failed: %v", err)
	}

	entries := decodeLogs(t, &buf)

	if len(entries) != 20 {
		t.Errorf("got %d log entries, want 20", len(entries))
	}

	for _, entry := range entries {
		if entry.GetDbUser() != "db_user" || entry.GetDbName() != "db_name" {
			t.Errorf("Unexpected user/db_name fields: user=%s, db=%s", entry.GetDbUser(), entry.GetDbName())
		}

		statement := entry.GetQueryStatement()
		template := entry.GetNormalizedQueryTemplate()

		switch template {
		case "SELECT * FROM users WHERE age > %v":
			var age int
			if _, err := fmt.Sscanf(statement, "SELECT * FROM users WHERE age > %d", &age); err != nil {
				t.Errorf("failed to parse age from statement %q: %v", statement, err)
			} else if age < 18 || age > 65 {
				t.Errorf("age value %d out of bounds [18, 65]", age)
			}
			if entry.GetIsGroundTruthAnomalous() {
				t.Errorf("Expected normal query label, got anomalous")
			}
		case "INSERT INTO logs VALUES (%v)":
			var val float64
			if _, err := fmt.Sscanf(statement, "INSERT INTO logs VALUES (%f)", &val); err != nil {
				t.Errorf("failed to parse val from statement %q: %v", statement, err)
			} else if val < 100.0 || val > 200.0 {
				t.Errorf("log value %f out of bounds [100.0, 200.0]", val)
			}
			if !entry.GetIsGroundTruthAnomalous() {
				t.Errorf("Expected anomalous query label, got normal")
			}
		default:
			t.Errorf("Unexpected query template generated: %q", template)
		}
	}
}

func TestGenerateLogsBucketSampling(t *testing.T) {
	backupFlags(t)

	tests := []struct {
		name              string
		configText        string
		startTime         int64
		endTime           int64
		trainEndTime      int64
		bucketSizeMinutes int
		verify            func(t *testing.T, entries []*genpb.AuditLogEntry)
		wantErr           bool
	}{
		{
			name: "single_bucket_training_no_spikes",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 10.0
			anomalous_spike_probability: 0.05
			anomalous_spike_multiplier: 2.0
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719882000000,
			trainEndTime:      1719882000000,
			bucketSizeMinutes: 60,
			verify: func(t *testing.T, entries []*genpb.AuditLogEntry) {
				if len(entries) == 0 {
					t.Fatalf("expected some logs to be generated, got 0")
				}
				for _, entry := range entries {
					if entry.GetIsGroundTruthAnomalous() {
						t.Errorf("expected normal query, got is_ground_truth_anomalous = true during training phase")
					}
					if entry.GetTimestampMs() < 1719878400000 || entry.GetTimestampMs() >= 1719882000000 {
						t.Errorf("timestamp %d out of bucket boundary [1719878400000, 1719882000000)", entry.GetTimestampMs())
					}
				}
			},
			wantErr: false,
		},
		{
			name: "multi_bucket_chronological_sorting",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 5.0
			anomalous_spike_probability: 0.05
			anomalous_spike_multiplier: 2.0
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719885600000,
			trainEndTime:      1719885600000,
			bucketSizeMinutes: 60,
			verify: func(t *testing.T, entries []*genpb.AuditLogEntry) {
				if len(entries) < 2 {
					return
				}
				for i := 0; i < len(entries)-1; i++ {
					if entries[i].GetTimestampMs() > entries[i+1].GetTimestampMs() {
						t.Errorf("log entries are not sorted chronologically: entries[%d] has timestamp %d, entries[%d] has timestamp %d", i, entries[i].GetTimestampMs(), i+1, entries[i+1].GetTimestampMs())
					}
				}
			},
			wantErr: false,
		},
		{
			name: "edge_case_empty_time_window_yields_zero_logs",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 10.0
			anomalous_spike_probability: 0.05
			anomalous_spike_multiplier: 2.0
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719878400000,
			trainEndTime:      1719878400000,
			bucketSizeMinutes: 60,
			verify: func(t *testing.T, entries []*genpb.AuditLogEntry) {
				if len(entries) != 0 {
					t.Errorf("expected 0 logs, got %d", len(entries))
				}
			},
			wantErr: false,
		},
		{
			name: "edge_case_testing_phase_spike_probability_one_forces_spikes",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 10.0
			anomalous_spike_probability: 1.0
			anomalous_spike_multiplier: 2.0
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719882000000,
			trainEndTime:      1719878400000,
			bucketSizeMinutes: 60,
			verify: func(t *testing.T, entries []*genpb.AuditLogEntry) {
				if len(entries) == 0 {
					t.Fatalf("expected logs to be generated, got 0")
				}
				for _, entry := range entries {
					if !entry.GetIsGroundTruthAnomalous() {
						t.Errorf("expected spike query label, got normal during spike testing bucket")
					}
				}
			},
			wantErr: false,
		},
		{
			name: "edge_case_training_phase_ignores_spike_probability",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 10.0
			anomalous_spike_probability: 1.0
			anomalous_spike_multiplier: 2.0
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719882000000,
			trainEndTime:      1719882000000,
			bucketSizeMinutes: 60,
			verify: func(t *testing.T, entries []*genpb.AuditLogEntry) {
				if len(entries) == 0 {
					t.Fatalf("expected logs to be generated, got 0")
				}
				for _, entry := range entries {
					if entry.GetIsGroundTruthAnomalous() {
						t.Errorf("expected normal query, got spike label during training phase despite 1.0 spike probability")
					}
				}
			},
			wantErr: false,
		},
		{
			name: "edge_case_nil_bucket_config_fails_validation",
			configText: `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
	}
}`,
			startTime:         1719878400000,
			endTime:           1719882000000,
			trainEndTime:      1719885600000,
			bucketSizeMinutes: 60,
			wantErr:           true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			*startTimeMs = test.startTime
			*endTimeMs = test.endTime
			*trainEndTimeMs = test.trainEndTime
			*bucketSizeMinutes = test.bucketSizeMinutes

			configs := &genpb.QueryTemplateConfigs{}
			if err := prototext.Unmarshal([]byte(test.configText), configs); err != nil {
				if test.wantErr {
					return
				}
				t.Fatalf("unmarshal failed: %v", err)
			}

			err := validateConfigs(configs)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error from validation, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}

			var buf bytes.Buffer
			rng := rand.New(rand.NewSource(42))
			if err := GenerateLogsBucketByBucket(configs, rng, &buf); err != nil {
				t.Fatalf("GenerateLogsBucketByBucket failed: %v", err)
			}

			entries := decodeLogs(t, &buf)
			test.verify(t, entries)
		})
	}
}

func TestGenerateLogs_WriterError(t *testing.T) {
	backupFlags(t)

	configTextWeighted := `
weighted_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		probability_weight: 1.0
	}
}`
	configsWeighted := parseProto(t, configTextWeighted, &genpb.QueryTemplateConfigs{})

	badWriter := &errorWriter{}
	rng := rand.New(rand.NewSource(42))

	if err := GenerateLogsWeighted(configsWeighted, 10, rng, badWriter); err == nil {
		t.Errorf("GenerateLogsWeighted expected error on bad writer, got nil")
	}

	*startTimeMs = 1719878400000
	*endTimeMs = 1719882000000
	*trainEndTimeMs = 1719882000000
	*bucketSizeMinutes = 60

	configTextBucket := `
bucket_sampling {
	query_template_configs: {
		template: {
			normalized_query_template: "SELECT 1"
		}
		bucket_config: {
			queries_per_bucket_mean: 10.0
		}
	}
}`
	configsBucket := parseProto(t, configTextBucket, &genpb.QueryTemplateConfigs{})

	if err := GenerateLogsBucketByBucket(configsBucket, rng, badWriter); err == nil {
		t.Errorf("GenerateLogsBucketByBucket expected error on bad writer, got nil")
	}
}
