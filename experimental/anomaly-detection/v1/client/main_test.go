package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"google.golang.org/protobuf/encoding/protodelim"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

func TestValidateFlags(t *testing.T) {
	// Save original values of all flags that might be mutated.
	origTestRatio := *testRatio
	origAnomalyThresholdRatio := *anomalyThresholdRatio
	origNumLogs := *numLogs
	origConfigPath := *configPath
	origAnomalyThresholdScales := *anomalyThresholdScalesFlag
	origScalingLogCounts := *scalingLogCountsFlag
	origTestRatioScales := *testRatioScalesFlag
	origStdDevMultiplierScales := *stdDevMultiplierScalesFlag
	origDetectionBucketSizeMinutesScales := *detectionBucketSizeMinutesScalesFlag
	origAnomalousSpikeMultiplierScales := *anomalousSpikeMultiplierScales
	origTrainDurationWeeksScales := *trainDurationWeeksScalesFlag
	origStartTimeMs := *startTimeMs
	origTrainEndTimeMs := *trainEndTimeMs
	origEndTimeMs := *endTimeMs
	origBucketSizeMinutes := *bucketSizeMinutes
	origPeriodMinutes := *periodMinutes
	origDetectionBucketSizeMinutes := *detectionBucketSizeMinutes
	origStdDevMultiplier := *stdDevMultiplier
	origSilentThresholdRatio := *silentThresholdRatio
	origTrainDurationDaysScales := *trainDurationDaysScalesFlag
	origSilentThresholdRatioScales := *silentThresholdRatioScales
	origWeekendTrafficMultiplierScales := *weekendTrafficMultiplierScales

	resetFlags := func() {
		*testRatio = origTestRatio
		*anomalyThresholdRatio = origAnomalyThresholdRatio
		*numLogs = origNumLogs
		*configPath = origConfigPath
		*anomalyThresholdScalesFlag = origAnomalyThresholdScales
		*scalingLogCountsFlag = origScalingLogCounts
		*testRatioScalesFlag = origTestRatioScales
		*stdDevMultiplierScalesFlag = origStdDevMultiplierScales
		*detectionBucketSizeMinutesScalesFlag = origDetectionBucketSizeMinutesScales
		*anomalousSpikeMultiplierScales = origAnomalousSpikeMultiplierScales
		*trainDurationWeeksScalesFlag = origTrainDurationWeeksScales
		*startTimeMs = origStartTimeMs
		*trainEndTimeMs = origTrainEndTimeMs
		*endTimeMs = origEndTimeMs
		*bucketSizeMinutes = origBucketSizeMinutes
		*periodMinutes = origPeriodMinutes
		*detectionBucketSizeMinutes = origDetectionBucketSizeMinutes
		*stdDevMultiplier = origStdDevMultiplier
		*silentThresholdRatio = origSilentThresholdRatio
		*trainDurationDaysScalesFlag = origTrainDurationDaysScales
		*silentThresholdRatioScales = origSilentThresholdRatioScales
		*weekendTrafficMultiplierScales = origWeekendTrafficMultiplierScales
	}

	defer resetFlags()

	tests := []struct {
		name    string
		mutate  func()
		wantErr bool
	}{
		{
			name:    "default_flags",
			mutate:  func() {},
			wantErr: false,
		},
		{
			name:    "invalid_test_ratio_large",
			mutate:  func() { *testRatio = 1.5 },
			wantErr: true,
		},
		{
			name:    "invalid_test_ratio_negative",
			mutate:  func() { *testRatio = -0.1 },
			wantErr: true,
		},
		{
			name:    "invalid_anomaly_threshold_ratio_large",
			mutate:  func() { *anomalyThresholdRatio = 1.0 },
			wantErr: true,
		},
		{
			name:    "invalid_anomaly_threshold_ratio_negative",
			mutate:  func() { *anomalyThresholdRatio = -0.05 },
			wantErr: true,
		},
		{
			name:    "invalid_num_logs_negative",
			mutate:  func() { *numLogs = -10 },
			wantErr: true,
		},
		{
			name:    "invalid_num_logs_zero",
			mutate:  func() { *numLogs = 0 },
			wantErr: true,
		},
		{
			name:    "invalid_config_path_empty",
			mutate:  func() { *configPath = "" },
			wantErr: true,
		},
		{
			name:    "invalid_anomaly_threshold_scales_format",
			mutate:  func() { *anomalyThresholdScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_anomaly_threshold_scales_out_of_range",
			mutate:  func() { *anomalyThresholdScalesFlag = []string{"1.5"} },
			wantErr: true,
		},
		{
			name:    "invalid_scaling_log_counts_format",
			mutate:  func() { *scalingLogCountsFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_scaling_log_counts_negative",
			mutate:  func() { *scalingLogCountsFlag = []string{"-10"} },
			wantErr: true,
		},
		{
			name:    "invalid_test_ratio_scales_format",
			mutate:  func() { *testRatioScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_test_ratio_scales_out_of_range",
			mutate:  func() { *testRatioScalesFlag = []string{"1.5"} },
			wantErr: true,
		},
		{
			name:    "invalid_stddev_multiplier_scales_format",
			mutate:  func() { *stdDevMultiplierScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_stddev_multiplier_scales_negative",
			mutate:  func() { *stdDevMultiplierScalesFlag = []string{"-0.5"} },
			wantErr: true,
		},
		{
			name:    "invalid_bucket_size_minutes_scales_format",
			mutate:  func() { *detectionBucketSizeMinutesScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_bucket_size_minutes_scales_negative",
			mutate:  func() { *detectionBucketSizeMinutesScalesFlag = []string{"-30"} },
			wantErr: true,
		},
		{
			name:    "invalid_anomalous_spike_multiplier_scales_format",
			mutate:  func() { *anomalousSpikeMultiplierScales = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_anomalous_spike_multiplier_scales_negative",
			mutate:  func() { *anomalousSpikeMultiplierScales = []string{"-2.0"} },
			wantErr: true,
		},
		{
			name:    "invalid_train_duration_weeks_scales_format",
			mutate:  func() { *trainDurationWeeksScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_train_duration_weeks_scales_negative",
			mutate:  func() { *trainDurationWeeksScalesFlag = []string{"-2"} },
			wantErr: true,
		},
		{
			name:    "invalid_start_time_ms_zero",
			mutate:  func() { *startTimeMs = 0 },
			wantErr: true,
		},
		{
			name:    "invalid_start_time_ms_negative",
			mutate:  func() { *startTimeMs = -100 },
			wantErr: true,
		},
		{
			name:    "invalid_train_end_time_ms_before_start",
			mutate:  func() { *trainEndTimeMs = *startTimeMs - 10 },
			wantErr: true,
		},
		{
			name:    "invalid_end_time_ms_before_train_end",
			mutate:  func() { *endTimeMs = *trainEndTimeMs - 10 },
			wantErr: true,
		},
		{
			name:    "invalid_bucket_size_minutes_zero",
			mutate:  func() { *bucketSizeMinutes = 0 },
			wantErr: true,
		},
		{
			name:    "invalid_bucket_size_minutes_negative",
			mutate:  func() { *bucketSizeMinutes = -10 },
			wantErr: true,
		},
		{
			name:    "invalid_start_time_ms_not_aligned",
			mutate:  func() { *startTimeMs = 1716199200001 },
			wantErr: true,
		},
		{
			name:    "invalid_train_end_time_ms_not_aligned",
			mutate:  func() { *trainEndTimeMs = 1722247200001 },
			wantErr: true,
		},
		{
			name:    "invalid_end_time_ms_not_aligned",
			mutate:  func() { *endTimeMs = 1723456800001 },
			wantErr: true,
		},
		{
			name:    "invalid_std_dev_multiplier_zero",
			mutate:  func() { *stdDevMultiplier = 0.0 },
			wantErr: true,
		},
		{
			name:    "invalid_std_dev_multiplier_negative",
			mutate:  func() { *stdDevMultiplier = -1.0 },
			wantErr: true,
		},
		{
			name:    "invalid_detection_bucket_size_minutes_zero",
			mutate:  func() { *detectionBucketSizeMinutes = 0 },
			wantErr: true,
		},
		{
			name:    "invalid_detection_bucket_size_minutes_negative",
			mutate:  func() { *detectionBucketSizeMinutes = -10 },
			wantErr: true,
		},
		{
			name:    "invalid_period_minutes_zero",
			mutate:  func() { *periodMinutes = 0 },
			wantErr: true,
		},
		{
			name:    "invalid_period_minutes_negative",
			mutate:  func() { *periodMinutes = -10 },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_zero",
			mutate:  func() { *silentThresholdRatio = 0.0 },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_one",
			mutate:  func() { *silentThresholdRatio = 1.0 },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_large",
			mutate:  func() { *silentThresholdRatio = 1.5 },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_negative",
			mutate:  func() { *silentThresholdRatio = -0.1 },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_scales_format",
			mutate:  func() { *silentThresholdRatioScales = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_silent_threshold_ratio_scales_out_of_range",
			mutate:  func() { *silentThresholdRatioScales = []string{"1.5"} },
			wantErr: true,
		},
		{
			name:    "invalid_train_duration_days_scales_format",
			mutate:  func() { *trainDurationDaysScalesFlag = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_train_duration_days_scales_negative",
			mutate:  func() { *trainDurationDaysScalesFlag = []string{"-5"} },
			wantErr: true,
		},
		{
			name:    "invalid_weekend_traffic_multiplier_scales_format",
			mutate:  func() { *weekendTrafficMultiplierScales = []string{"abc"} },
			wantErr: true,
		},
		{
			name:    "invalid_weekend_traffic_multiplier_scales_negative",
			mutate:  func() { *weekendTrafficMultiplierScales = []string{"-0.5"} },
			wantErr: true,
		},
		{
			name: "invalid_period_detection_bucket_relation",
			mutate: func() {
				*periodMinutes = 60
				*detectionBucketSizeMinutes = 45
			},
			wantErr: true,
		},
		{
			name: "invalid_bucket_size_minutes_scales_indivisible",
			mutate: func() {
				*periodMinutes = 60
				*detectionBucketSizeMinutesScalesFlag = []string{"10", "25", "30"}
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetFlags()
			test.mutate()
			if err := validateFlags(); (err != nil) != test.wantErr {
				t.Errorf("validateFlags() got error = %v, wantErr = %v", err, test.wantErr)
			}
		})
	}
}

func TestMustParseFloatList(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []float64
	}{
		{
			name:  "valid_floats",
			input: []string{"0.05", "1.23", "45.6"},
			want:  []float64{0.05, 1.23, 45.6},
		},
		{
			name:  "empty_list",
			input: []string{},
			want:  nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mustParseFloatList(test.input)
			if len(got) != len(test.want) {
				t.Errorf("mustParseFloatList(%v) got len %d, want %d", test.input, len(got), len(test.want))
				return
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("mustParseFloatList(%v) at index %d got %v, want %v", test.input, i, got[i], test.want[i])
				}
			}
		})
	}

	t.Run("invalid_string_panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("mustParseFloatList() expected panic on invalid input, but it did not panic")
			}
		}()
		mustParseFloatList([]string{"invalid"})
	})
}

func TestMustParseIntList(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []int
	}{
		{
			name:  "valid_ints",
			input: []string{"1", "42", "100"},
			want:  []int{1, 42, 100},
		},
		{
			name:  "empty_list",
			input: []string{},
			want:  nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mustParseIntList(test.input)
			if len(got) != len(test.want) {
				t.Errorf("mustParseIntList(%v) got len %d, want %d", test.input, len(got), len(test.want))
				return
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Errorf("mustParseIntList(%v) at index %d got %v, want %v", test.input, i, got[i], test.want[i])
				}
			}
		})
	}

	t.Run("invalid_string_panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("mustParseIntList() expected panic on invalid input, but it did not panic")
			}
		}()
		mustParseIntList([]string{"abc"})
	})
}

func TestSplitLogsByRatioStreaming(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		logCount      int
		ratio         float64
		wantErr       bool
		wantTrainSize int
		wantTestSize  int
	}{
		{
			name:          "ratio_90_percent",
			logCount:      100,
			ratio:         0.90,
			wantErr:       false,
			wantTrainSize: 10,
			wantTestSize:  90,
		},
		{
			name:          "ratio_20_percent",
			logCount:      10,
			ratio:         0.20,
			wantErr:       false,
			wantTrainSize: 8,
			wantTestSize:  2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logsPath := filepath.Join(tmpDir, "logs.binproto")
			trainPath := filepath.Join(tmpDir, "train.binproto")
			testPath := filepath.Join(tmpDir, "test.binproto")

			// Setup raw log entries file.
			file, err := os.Create(logsPath)
			if err != nil {
				t.Fatalf("failed to create logs file: %v", err)
			}
			writer := bufio.NewWriter(file)
			for i := 0; i < test.logCount; i++ {
				entry := &auditpb.AuditLogEntry{}
				entry.SetNormalizedQueryTemplate("SELECT " + strconv.Itoa(i))
				if _, err := protodelim.MarshalTo(writer, entry); err != nil {
					t.Fatalf("failed to write test log: %v", err)
				}
			}
			writer.Flush()
			file.Close()

			err = splitLogsByRatioStreaming(ctx, logsPath, trainPath, testPath, test.ratio, test.logCount)
			if (err != nil) != test.wantErr {
				t.Fatalf("splitLogsByRatioStreaming() error = %v, wantErr = %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}

			// Verify train logs read back.
			trainFile, err := os.Open(trainPath)
			if err != nil {
				t.Fatalf("failed to open train file: %v", err)
			}
			trainReader := bufio.NewReader(trainFile)
			trainCount := 0
			for {
				entry := &auditpb.AuditLogEntry{}
				err := protodelim.UnmarshalFrom(trainReader, entry)
				if err != nil {
					break
				}
				expectedTemplate := "SELECT " + strconv.Itoa(trainCount)
				if entry.GetNormalizedQueryTemplate() != expectedTemplate {
					t.Errorf("train[%d] template = %q, want %q", trainCount, entry.GetNormalizedQueryTemplate(), expectedTemplate)
				}
				trainCount++
			}
			trainFile.Close()

			if trainCount != test.wantTrainSize {
				t.Errorf("train size = %d, want %d", trainCount, test.wantTrainSize)
			}

			// Verify test logs read back.
			testFile, err := os.Open(testPath)
			if err != nil {
				t.Fatalf("failed to open test file: %v", err)
			}
			testReader := bufio.NewReader(testFile)
			testCount := 0
			for {
				entry := &auditpb.AuditLogEntry{}
				err := protodelim.UnmarshalFrom(testReader, entry)
				if err != nil {
					break
				}
				expectedTemplate := "SELECT " + strconv.Itoa(test.wantTrainSize+testCount)
				if entry.GetNormalizedQueryTemplate() != expectedTemplate {
					t.Errorf("test[%d] template = %q, want %q", testCount, entry.GetNormalizedQueryTemplate(), expectedTemplate)
				}
				testCount++
			}
			testFile.Close()

			if testCount != test.wantTestSize {
				t.Errorf("test size = %d, want %d", testCount, test.wantTestSize)
			}
		})
	}
}

func TestReadConfig(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		content     string
		writeConfig bool
		wantErr     bool
	}{
		{
			name: "valid_spike_traffic_config",
			content: `
bucket_sampling {
  query_template_configs {
    template {
      normalized_query_template: "SELECT * FROM users WHERE id = %v"
    }
    bucket_config {
      queries_per_bucket_mean: 100.0
    }
  }
}
`,
			writeConfig: true,
			wantErr:     false,
		},
		{
			name:        "invalid_textproto_format",
			content:     `invalid key-value formats`,
			writeConfig: true,
			wantErr:     true,
		},
		{
			name:        "missing_file",
			content:     "",
			writeConfig: false,
			wantErr:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.textproto")

			if test.writeConfig {
				if err := os.WriteFile(configPath, []byte(test.content), 0644); err != nil {
					t.Fatalf("failed to write dev config textproto: %v", err)
				}
			} else {
				configPath = filepath.Join(tmpDir, "nonexistent.textproto")
			}

			got, err := readConfig(ctx, configPath)
			if (err != nil) != test.wantErr {
				t.Errorf("readConfig() error = %v, wantErr = %v", err, test.wantErr)
			}
			if !test.wantErr && got.GetBucketSampling() == nil {
				t.Errorf("readConfig() returned empty config structure")
			}
		})
	}
}

func TestSplitLogsByTimeStreaming(t *testing.T) {
	ctx := context.Background()

	// Base timestamp representing the simulated timeline (Jul 1 2024 boundary)
	baseTimeMs := int64(1719830400000)
	const weekMs = int64(7 * 24 * 3600 * 1000)

	tests := []struct {
		name               string
		logTimestamps      []int64
		trainDurationWeeks int
		partitionTimeMs    int64
		wantTrainCount     int
		wantTestCount      int
	}{
		// Slices training logs using the 4-week training window.
		{
			name: "train_duration_four_weeks",
			logTimestamps: []int64{
				baseTimeMs,            // Week 1 (Train)
				baseTimeMs + weekMs,   // Week 2 (Train)
				baseTimeMs + weekMs*3, // Week 4 (Train)
				baseTimeMs + weekMs*4, // Week 5 (Test, >= partition)
				baseTimeMs + weekMs*5, // Week 6 (Test, >= partition)
			},
			trainDurationWeeks: 4,
			partitionTimeMs:    baseTimeMs + weekMs*4,
			wantTrainCount:     3,
			wantTestCount:      2,
		},
		// Slices training logs dynamically for a tighter 1-week training duration.
		{
			name: "train_duration_one_week",
			logTimestamps: []int64{
				baseTimeMs,            // Week 1 (Ignored: older than 1 week)
				baseTimeMs + weekMs*3, // Week 4 (Train: within 1 week of partition)
				baseTimeMs + weekMs*4, // Week 5 (Test)
			},
			trainDurationWeeks: 1,
			partitionTimeMs:    baseTimeMs + weekMs*4,
			wantTrainCount:     1,
			wantTestCount:      1,
		},
		// Default case (trainDurationWeeks = 0) trains on the entire historical timeline before partition.
		{
			name: "train_duration_all_history",
			logTimestamps: []int64{
				baseTimeMs,             // Week 1 (Train)
				baseTimeMs + weekMs*10, // Week 11 (Train)
				baseTimeMs + weekMs*20, // Week 21 (Test)
			},
			trainDurationWeeks: 0,
			partitionTimeMs:    baseTimeMs + weekMs*20,
			wantTrainCount:     2,
			wantTestCount:      1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logsPath := filepath.Join(tmpDir, "logs.binproto")
			trainLogAbs := filepath.Join(tmpDir, "train.binproto")
			testLogAbs := filepath.Join(tmpDir, "test.binproto")

			// Create raw logs.binproto file with specific timestamps
			file, err := os.Create(logsPath)
			if err != nil {
				t.Fatalf("failed to create logs file: %v", err)
			}
			writer := bufio.NewWriter(file)
			for i, ts := range test.logTimestamps {
				entry := &auditpb.AuditLogEntry{}
				entry.SetTimestampMs(ts)
				entry.SetNormalizedQueryTemplate("SELECT " + strconv.Itoa(i))
				if _, err := protodelim.MarshalTo(writer, entry); err != nil {
					t.Fatalf("failed to write audit log entry: %v", err)
				}
			}
			writer.Flush()
			file.Close()

			// Perform streaming split
			err = splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, test.partitionTimeMs, test.trainDurationWeeks*7)
			if err != nil {
				t.Fatalf("splitLogsByTimeStreaming failed: %v", err)
			}

			// Validate training outputs count
			trainFile, err := os.Open(trainLogAbs)
			if err != nil {
				t.Fatalf("failed to open train file: %v", err)
			}
			trainReader := bufio.NewReader(trainFile)
			gotTrainCount := 0
			for {
				entry := &auditpb.AuditLogEntry{}
				if err := protodelim.UnmarshalFrom(trainReader, entry); err != nil {
					break
				}
				gotTrainCount++
			}
			trainFile.Close()

			if gotTrainCount != test.wantTrainCount {
				t.Errorf("train count got %d, want %d", gotTrainCount, test.wantTrainCount)
			}

			// Validate testing outputs count
			testFile, err := os.Open(testLogAbs)
			if err != nil {
				t.Fatalf("failed to open test file: %v", err)
			}
			testReader := bufio.NewReader(testFile)
			gotTestCount := 0
			for {
				entry := &auditpb.AuditLogEntry{}
				if err := protodelim.UnmarshalFrom(testReader, entry); err != nil {
					break
				}
				gotTestCount++
			}
			testFile.Close()

			if gotTestCount != test.wantTestCount {
				t.Errorf("test count got %d, want %d", gotTestCount, test.wantTestCount)
			}
		})
	}
}
