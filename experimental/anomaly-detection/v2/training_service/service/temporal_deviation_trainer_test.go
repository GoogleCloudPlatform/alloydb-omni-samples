package service

import (
	"math"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
)

func TestNewTemporalDeviationTrainer(t *testing.T) {
	tests := []struct {
		name                 string
		silentThresholdRatio float64
		bucketDuration       time.Duration
		cycleDuration        time.Duration
		wantErr              bool
	}{
		{
			name:                 "valid_inputs",
			silentThresholdRatio: 0.1,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              false,
		},
		{
			name:                 "ratio_near_boundaries",
			silentThresholdRatio: 0.001,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              false,
		},
		{
			name:                 "bucket_equals_period",
			silentThresholdRatio: 0.1,
			bucketDuration:       1 * time.Hour,
			cycleDuration:        1 * time.Hour,
			wantErr:              false,
		},
		{
			name:                 "zero_ratio",
			silentThresholdRatio: 0.0,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "negative_ratio",
			silentThresholdRatio: -0.1,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "one_ratio",
			silentThresholdRatio: 1.0,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "greater_than_one_ratio",
			silentThresholdRatio: 1.5,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "zero_bucket_size",
			silentThresholdRatio: 0.1,
			bucketDuration:       0,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "negative_bucket_size",
			silentThresholdRatio: 0.1,
			bucketDuration:       -10 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "zero_period",
			silentThresholdRatio: 0.1,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        0,
			wantErr:              true,
		},
		{
			name:                 "negative_period",
			silentThresholdRatio: 0.1,
			bucketDuration:       10 * time.Minute,
			cycleDuration:        -1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "bucket_exceeds_period",
			silentThresholdRatio: 0.1,
			bucketDuration:       2 * time.Hour,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
		{
			name:                 "indivisible_bucket",
			silentThresholdRatio: 0.1,
			bucketDuration:       7 * time.Minute,
			cycleDuration:        1 * time.Hour,
			wantErr:              true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewTemporalDeviationTrainer(test.silentThresholdRatio, test.bucketDuration, test.cycleDuration)
			if (err != nil) != test.wantErr {
				t.Errorf("NewTemporalDeviationTrainer(%v, %v, %v) error = %v, wantErr %v", test.silentThresholdRatio, test.bucketDuration, test.cycleDuration, err, test.wantErr)
			}
			if !test.wantErr {
				if trainer == nil {
					t.Fatalf("NewTemporalDeviationTrainer() returned nil trainer for valid inputs")
				}
				if trainer.cycleBucketRequestCounts == nil || trainer.userTemplateTotalCounts == nil {
					t.Errorf("internal count maps were not initialized")
				}
				wantSlots := int64(test.cycleDuration / test.bucketDuration)
				if trainer.bucketsPerCycle != wantSlots {
					t.Errorf("bucketsPerCycle = %d, want %d", trainer.bucketsPerCycle, wantSlots)
				}
			}
		})
	}
}

func TestTemporalDeviationTrainer_Count(t *testing.T) {
	bucketDuration := 10 * time.Minute
	cycleDuration := 1 * time.Hour

	feature1 := common.ExtractedLogFeatures{
		DbUser:        "alice",
		QueryTemplate: "SELECT * FROM users",
		TimestampMs:   0, // Slot 0
	}
	feature2Slot0 := common.ExtractedLogFeatures{
		DbUser:        "alice",
		QueryTemplate: "SELECT * FROM users",
		TimestampMs:   5 * 60 * 1000, // Slot 0
	}
	featureSlot1 := common.ExtractedLogFeatures{
		DbUser:        "alice",
		QueryTemplate: "SELECT * FROM users",
		TimestampMs:   10 * 60 * 1000, // Slot 1
	}
	featureEmpty := common.ExtractedLogFeatures{
		DbUser:        "",
		QueryTemplate: "",
		TimestampMs:   0,
	}

	tests := []struct {
		name                 string
		features             []common.ExtractedLogFeatures
		wantTotalCounts      map[userTemplateKey]int
		wantSlotCountSamples map[common.CycleBucketKey]int
	}{
		{
			name:     "single_request",
			features: []common.ExtractedLogFeatures{feature1},
			wantTotalCounts: map[userTemplateKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users"}: 1,
			},
			wantSlotCountSamples: map[common.CycleBucketKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users", BucketIndex: 0}: 1,
			},
		},
		{
			name:     "multiple_requests_same_slot",
			features: []common.ExtractedLogFeatures{feature1, feature2Slot0},
			wantTotalCounts: map[userTemplateKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users"}: 2,
			},
			wantSlotCountSamples: map[common.CycleBucketKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users", BucketIndex: 0}: 2,
			},
		},
		{
			name:     "multiple_requests_different_slots",
			features: []common.ExtractedLogFeatures{feature1, featureSlot1},
			wantTotalCounts: map[userTemplateKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users"}: 2,
			},
			wantSlotCountSamples: map[common.CycleBucketKey]int{
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users", BucketIndex: 0}: 1,
				{DbUser: "alice", QueryTemplate: "SELECT * FROM users", BucketIndex: 1}: 1,
			},
		},
		{
			name:     "empty_user_or_template",
			features: []common.ExtractedLogFeatures{featureEmpty},
			wantTotalCounts: map[userTemplateKey]int{
				{DbUser: "", QueryTemplate: ""}: 1,
			},
			wantSlotCountSamples: map[common.CycleBucketKey]int{
				{DbUser: "", QueryTemplate: "", BucketIndex: 0}: 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewTemporalDeviationTrainer(0.1, bucketDuration, cycleDuration)
			if err != nil {
				t.Fatalf("failed to create trainer: %v", err)
			}
			for _, f := range test.features {
				trainer.Count(f)
			}

			for key, wantCount := range test.wantTotalCounts {
				if got := trainer.userTemplateTotalCounts[key]; got != wantCount {
					t.Errorf("userTemplateTotalCounts[%v] got %d, want %d", key, got, wantCount)
				}
			}
			for slotKey, wantCount := range test.wantSlotCountSamples {
				if got := trainer.cycleBucketRequestCounts[slotKey]; got != wantCount {
					t.Errorf("cycleBucketRequestCounts[%v] got %d, want %d", slotKey, got, wantCount)
				}
			}
		})
	}
}

func TestTemporalDeviationTrainer_BuildModel(t *testing.T) {
	bucketDuration := 10 * time.Minute
	cycleDuration := 1 * time.Hour

	tests := []struct {
		name                 string
		silentThresholdRatio float64
		setupTrainer         func(trainer *TemporalDeviationTrainer)
		wantSilentSlotsCount int
		checkSlotConfidence  map[common.CycleBucketKey]float64
		checkSlotNotSilent   []common.CycleBucketKey
		wantTotalLogCount    int64
		wantMinTimestamp     int64
		wantMaxTimestamp     int64
	}{
		{
			name:                 "build_with_silent_slots",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				for i := 0; i < 100; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   0,
					})
				}
			},
			wantSilentSlotsCount: 5,
			checkSlotConfidence: map[common.CycleBucketKey]float64{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 1}: 1.0,
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 5}: 1.0,
			},
			checkSlotNotSilent: []common.CycleBucketKey{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0},
			},
			wantTotalLogCount: 100,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  0,
		},
		{
			name:                 "partially_silent_slot",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				for i := 0; i < 95; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   0,
					})
				}
				for i := 0; i < 5; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   10 * 60 * 1000,
					})
				}
			},
			wantSilentSlotsCount: 5,
			checkSlotConfidence: map[common.CycleBucketKey]float64{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 1}: 0.5,
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 2}: 1.0,
			},
			checkSlotNotSilent: []common.CycleBucketKey{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0},
			},
			wantTotalLogCount: 100,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  600000,
		},
		{
			name:                 "no_data_empty_trainer",
			silentThresholdRatio: 0.1,
			setupTrainer:         func(trainer *TemporalDeviationTrainer) {},
			wantSilentSlotsCount: 0,
			wantTotalLogCount:    0,
			wantMinTimestamp:     0,
			wantMaxTimestamp:     0,
		},
		{
			name:                 "zero_total_count_skipped",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				trainer.userTemplateTotalCounts[userTemplateKey{DbUser: "bob", QueryTemplate: "DELETE"}] = 0
			},
			wantSilentSlotsCount: 0,
			wantTotalLogCount:    0,
			wantMinTimestamp:     0,
			wantMaxTimestamp:     0,
		},
		{
			name:                 "boundary_count_equals_threshold",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				for i := 0; i < 90; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   0, // Slot 0
					})
				}
				for i := 0; i < 10; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   10 * 60 * 1000, // Slot 1
					})
				}
			},
			wantSilentSlotsCount: 4,
			checkSlotNotSilent: []common.CycleBucketKey{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0},
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 1},
			},
			wantTotalLogCount: 100,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  600000,
		},
		{
			name:                 "all_slots_active_no_silent",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				for slot := int64(0); slot < 6; slot++ {
					for i := 0; i < 20; i++ {
						trainer.Count(common.ExtractedLogFeatures{
							DbUser:        "alice",
							QueryTemplate: "SELECT",
							TimestampMs:   slot * 10 * 60 * 1000,
						})
					}
				}
			},
			wantSilentSlotsCount: 0,
			wantTotalLogCount:    120,
			wantMinTimestamp:     0,
			wantMaxTimestamp:     3000000,
		},
		{
			name:                 "multiple_user_templates",
			silentThresholdRatio: 0.1,
			setupTrainer: func(trainer *TemporalDeviationTrainer) {
				for i := 0; i < 100; i++ {
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "alice",
						QueryTemplate: "SELECT",
						TimestampMs:   0,
					})
					trainer.Count(common.ExtractedLogFeatures{
						DbUser:        "bob",
						QueryTemplate: "INSERT",
						TimestampMs:   0,
					})
				}
			},
			wantSilentSlotsCount: 10, // 5 silent slots for alice, 5 for bob
			checkSlotConfidence: map[common.CycleBucketKey]float64{
				{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 1}: 1.0,
				{DbUser: "bob", QueryTemplate: "INSERT", BucketIndex: 1}:   1.0,
			},
			wantTotalLogCount: 200,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewTemporalDeviationTrainer(test.silentThresholdRatio, bucketDuration, cycleDuration)
			if err != nil {
				t.Fatalf("failed to create trainer: %v", err)
			}
			test.setupTrainer(trainer)

			modelUntyped := trainer.BuildModel()
			if modelUntyped == nil {
				t.Fatalf("BuildModel() returned nil")
			}

			if !modelUntyped.HasTemporalDeviation() {
				t.Fatalf("BuildModel() did not return TemporalDeviation case")
			}
			model := modelUntyped.GetTemporalDeviation().GetModelData()

			gotSilentSlots := model.GetEntries()
			if len(gotSilentSlots) != test.wantSilentSlotsCount {
				t.Errorf("len(silentSlots) got %d, want %d", len(gotSilentSlots), test.wantSilentSlotsCount)
			}

			gotMap := make(map[common.CycleBucketKey]float64)
			for _, entry := range gotSilentSlots {
				k := entry.GetKey()
				key := common.CycleBucketKey{
					DbUser:        k.GetDbUser(),
					QueryTemplate: k.GetQueryTemplate(),
					BucketIndex:   k.GetBucketIndex(),
				}
				gotMap[key] = entry.GetConfidenceScore()
			}

			for slotKey, wantScore := range test.checkSlotConfidence {
				gotScore, exists := gotMap[slotKey]
				if !exists {
					t.Errorf("slotKey %v expected in silentSlots but not found", slotKey)
				} else if math.Abs(gotScore-wantScore) > 1e-9 {
					t.Errorf("silentSlots[%v] score got %f, want %f", slotKey, gotScore, wantScore)
				}
			}

			for _, slotKey := range test.checkSlotNotSilent {
				if _, exists := gotMap[slotKey]; exists {
					t.Errorf("slotKey %v was NOT expected in silentSlots, but found", slotKey)
				}
			}

			if model.GetCycleConfig().GetBucketDuration().AsDuration() != bucketDuration {
				t.Errorf("bucketDuration got %v, want %v", model.GetCycleConfig().GetBucketDuration().AsDuration(), bucketDuration)
			}
			if model.GetCycleConfig().GetCycleDuration().AsDuration() != cycleDuration {
				t.Errorf("cycleDuration got %v, want %v", model.GetCycleConfig().GetCycleDuration().AsDuration(), cycleDuration)
			}

			// Verify Metadata
			if !modelUntyped.HasMetadata() {
				t.Fatalf("expected model to have metadata")
			}
			metadata := modelUntyped.GetMetadata()
			if metadata.GetTrainingTime() == nil {
				t.Errorf("expected TrainingTime to be set")
			} else {
				dt := time.Since(metadata.GetTrainingTime().AsTime())
				if dt < 0 || dt > 10*time.Second {
					t.Errorf("TrainingTime %v is not recent (delta: %v)", metadata.GetTrainingTime().AsTime(), dt)
				}
			}

			if test.wantTotalLogCount > 0 {
				if metadata.GetTotalLogEntriesProcessed() != test.wantTotalLogCount {
					t.Errorf("TotalLogEntriesProcessed got %d, want %d", metadata.GetTotalLogEntriesProcessed(), test.wantTotalLogCount)
				}
				if metadata.GetDataStartTime() == nil {
					t.Errorf("expected DataStartTime to be set")
				} else if metadata.GetDataStartTime().AsTime().UnixMilli() != test.wantMinTimestamp {
					t.Errorf("DataStartTime got %v, want %v", metadata.GetDataStartTime().AsTime().UnixMilli(), test.wantMinTimestamp)
				}
				if metadata.GetDataEndTime() == nil {
					t.Errorf("expected DataEndTime to be set")
				} else if metadata.GetDataEndTime().AsTime().UnixMilli() != test.wantMaxTimestamp {
					t.Errorf("DataEndTime got %v, want %v", metadata.GetDataEndTime().AsTime().UnixMilli(), test.wantMaxTimestamp)
				}
				if modelUntyped.GetTemporalDeviation().GetConfig().GetSilentThresholdRatio() != test.silentThresholdRatio {
					t.Errorf("SilentThresholdRatio got %f, want %f", modelUntyped.GetTemporalDeviation().GetConfig().GetSilentThresholdRatio(), test.silentThresholdRatio)
				}
			} else {
				if metadata.GetTotalLogEntriesProcessed() != 0 {
					t.Errorf("TotalLogEntriesProcessed got %d, want 0", metadata.GetTotalLogEntriesProcessed())
				}
				if metadata.GetDataStartTime() != nil {
					t.Errorf("expected DataStartTime to be nil, got %v", metadata.GetDataStartTime())
				}
				if metadata.GetDataEndTime() != nil {
					t.Errorf("expected DataEndTime to be nil, got %v", metadata.GetDataEndTime())
				}
			}
		})
	}
}
