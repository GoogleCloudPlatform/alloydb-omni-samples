package service

import (
	"math"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
)

func TestNewVolumetricSpikeTrainer(t *testing.T) {
	tests := []struct {
		name             string
		stdDevMultiplier float64
		bucketDuration   time.Duration
		cycleDuration    time.Duration
		wantErr          bool
	}{
		{
			name:             "validWeekly",
			stdDevMultiplier: 3.0,
			bucketDuration:   60 * time.Minute,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          false,
		},
		{
			name:             "validDaily",
			stdDevMultiplier: 3.0,
			bucketDuration:   30 * time.Minute,
			cycleDuration:    24 * time.Hour,
			wantErr:          false,
		},
		{
			name:             "negativeMultiplier",
			stdDevMultiplier: -1.0,
			bucketDuration:   60 * time.Minute,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "zeroMultiplier",
			stdDevMultiplier: 0.0,
			bucketDuration:   60 * time.Minute,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "zeroBucketSize",
			stdDevMultiplier: 3.0,
			bucketDuration:   0,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "subMillisecondBucketSize",
			stdDevMultiplier: 3.0,
			bucketDuration:   500 * time.Microsecond,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "bucketExceedsPeriod",
			stdDevMultiplier: 3.0,
			bucketDuration:   25 * time.Hour,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "indivisibleBucket",
			stdDevMultiplier: 3.0,
			bucketDuration:   5 * time.Hour,
			cycleDuration:    7 * 24 * time.Hour,
			wantErr:          true,
		},
		{
			name:             "zeroPeriod",
			stdDevMultiplier: 3.0,
			bucketDuration:   60 * time.Minute,
			cycleDuration:    0,
			wantErr:          true,
		},
		{
			name:             "negativePeriod",
			stdDevMultiplier: 3.0,
			bucketDuration:   60 * time.Minute,
			cycleDuration:    -7 * 24 * time.Hour,
			wantErr:          true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewVolumetricSpikeTrainer(test.stdDevMultiplier, test.bucketDuration, test.cycleDuration)
			if (err != nil) != test.wantErr {
				t.Errorf("NewVolumetricSpikeTrainer(%v, %v, %v) error = %v, wantErr %v", test.stdDevMultiplier, test.bucketDuration, test.cycleDuration, err, test.wantErr)
			}
		})
	}
}

func TestVolumetricSpikeTrainer_Count(t *testing.T) {
	tests := []struct {
		name           string
		features       []common.ExtractedLogFeatures
		wantCounts     map[common.AbsoluteBucketKey]int
		bucketDuration time.Duration
	}{
		{
			name: "multipleBuckets",
			features: []common.ExtractedLogFeatures{
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 1000},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 2000},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 7200000},
			},
			bucketDuration: time.Hour,
			wantCounts: map[common.AbsoluteBucketKey]int{
				{DbUser: "user1", QueryTemplate: "SELECT 1", AbsoluteBucketIndex: 0}: 2,
				{DbUser: "user1", QueryTemplate: "SELECT 1", AbsoluteBucketIndex: 2}: 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewVolumetricSpikeTrainer(3.0, test.bucketDuration, 7*24*time.Hour)
			if err != nil {
				t.Fatalf("failed to initialize trainer: %v", err)
			}
			for _, f := range test.features {
				trainer.Count(f)
			}

			if len(trainer.bucketRequestCounts) != len(test.wantCounts) {
				t.Errorf("bucketRequestCounts map size got %d, want %d", len(trainer.bucketRequestCounts), len(test.wantCounts))
			}
			for k, wantVal := range test.wantCounts {
				gotVal, exists := trainer.bucketRequestCounts[k]
				if !exists {
					t.Errorf("expected bucketRequestCounts to contain %+v", k)
				} else if gotVal != wantVal {
					t.Errorf("count for %+v got %d, want %d", k, gotVal, wantVal)
				}
			}
		})
	}
}

func TestVolumetricSpikeTrainer_BuildModel(t *testing.T) {
	tests := []struct {
		name              string
		stdDevMultiplier  float64
		bucketDuration    time.Duration
		cycleDuration     time.Duration
		features          []common.ExtractedLogFeatures
		wantBaselines     map[common.CycleBucketKey]VolumetricBaseline
		wantTotalLogCount int64
		wantMinTimestamp  int64
		wantMaxTimestamp  int64
	}{
		{
			name:              "emptyCounts",
			stdDevMultiplier:  3.0,
			bucketDuration:    time.Hour,
			cycleDuration:     7 * 24 * time.Hour,
			features:          nil,
			wantBaselines:     map[common.CycleBucketKey]VolumetricBaseline{},
			wantTotalLogCount: 0,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  0,
		},
		{
			// Verifies Mean and StdDev calculations on periodic cycles with missing periods padded as zeros.
			name:             "uncountedWeeksPatchedAsZeros",
			stdDevMultiplier: 2.0,
			bucketDuration:   time.Hour,
			cycleDuration:    7 * 24 * time.Hour,
			features: []common.ExtractedLogFeatures{
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 0},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 0},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 1209600000}, // 2 weeks later
			},
			wantBaselines: map[common.CycleBucketKey]VolumetricBaseline{
				{DbUser: "user1", BucketIndex: 0, QueryTemplate: "SELECT 1"}: {
					Mean:            1.0,
					StdDev:          math.Sqrt(2.0 / 3.0),
					UpperThreshold:  1.0 + 2.0*math.Sqrt(2.0/3.0),
					DeviationMargin: 2.0 * math.Sqrt(2.0/3.0),
				},
			},
			wantTotalLogCount: 3,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  1209600000,
		},
		{
			// Verifies baseline calculation for non-weekly custom periods.
			name:             "customPeriod",
			stdDevMultiplier: 2.0,
			bucketDuration:   30 * time.Minute,
			cycleDuration:    2 * time.Hour,
			features: []common.ExtractedLogFeatures{
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 0},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 0},
				{DbUser: "user1", QueryTemplate: "SELECT 1", TimestampMs: 7200000}, // 2 hours later
			},
			wantBaselines: map[common.CycleBucketKey]VolumetricBaseline{
				{DbUser: "user1", BucketIndex: 0, QueryTemplate: "SELECT 1"}: {
					Mean:            1.5,
					StdDev:          0.5,
					UpperThreshold:  2.5,
					DeviationMargin: 1.0,
				},
			},
			wantTotalLogCount: 3,
			wantMinTimestamp:  0,
			wantMaxTimestamp:  7200000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewVolumetricSpikeTrainer(test.stdDevMultiplier, test.bucketDuration, test.cycleDuration)
			if err != nil {
				t.Fatalf("failed to initialize trainer: %v", err)
			}
			for _, f := range test.features {
				trainer.Count(f)
			}

			model := trainer.BuildModel()
			if model == nil {
				t.Fatalf("BuildModel() returned nil")
			}

			if !model.HasVolumetricSpike() {
				t.Fatalf("BuildModel() did not return VolumetricSpike case")
			}
			baselineModel := model.GetVolumetricSpike().GetModelData()

			gotBaselines := baselineModel.GetEntries()
			if len(gotBaselines) != len(test.wantBaselines) {
				t.Errorf("baselines count got %d, want %d", len(gotBaselines), len(test.wantBaselines))
			}

			gotMap := make(map[common.CycleBucketKey]VolumetricBaseline)
			for _, entry := range gotBaselines {
				k := entry.GetKey()
				v := entry.GetValue()
				key := common.CycleBucketKey{
					DbUser:        k.GetDbUser(),
					QueryTemplate: k.GetQueryTemplate(),
					BucketIndex:   k.GetBucketIndex(),
				}
				val := VolumetricBaseline{
					Mean:            v.GetMean(),
					StdDev:          v.GetStdDev(),
					UpperThreshold:  v.GetUpperThreshold(),
					DeviationMargin: v.GetDeviationMargin(),
				}
				gotMap[key] = val
			}

			for k, wantVal := range test.wantBaselines {
				gotVal, exists := gotMap[k]
				if !exists {
					t.Errorf("expected baselines to contain %+v", k)
				} else {
					const delta = 1e-6
					if math.Abs(gotVal.Mean-wantVal.Mean) > delta ||
						math.Abs(gotVal.StdDev-wantVal.StdDev) > delta ||
						math.Abs(gotVal.UpperThreshold-wantVal.UpperThreshold) > delta ||
						math.Abs(gotVal.DeviationMargin-wantVal.DeviationMargin) > delta {
						t.Errorf("baseline for %+v got %+v, want %+v", k, gotVal, wantVal)
					}
				}
			}

			if baselineModel.GetCycleConfig().GetBucketDuration().AsDuration() != test.bucketDuration {
				t.Errorf("bucketDuration got %v, want %v", baselineModel.GetCycleConfig().GetBucketDuration().AsDuration(), test.bucketDuration)
			}
			if baselineModel.GetCycleConfig().GetCycleDuration().AsDuration() != test.cycleDuration {
				t.Errorf("cycleDuration got %v, want %v", baselineModel.GetCycleConfig().GetCycleDuration().AsDuration(), test.cycleDuration)
			}

			// Verify Metadata
			if !model.HasMetadata() {
				t.Fatalf("expected model to have metadata")
			}
			metadata := model.GetMetadata()
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
				if model.GetVolumetricSpike().GetConfig().GetStdDevMultiplier() != test.stdDevMultiplier {
					t.Errorf("StdDevMultiplier got %f, want %f", model.GetVolumetricSpike().GetConfig().GetStdDevMultiplier(), test.stdDevMultiplier)
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

func TestCountExpectedCycles(t *testing.T) {
	tests := []struct {
		name            string
		minIdx          int64
		maxIdx          int64
		slotIdx         int64
		bucketsPerCycle int64
		want            int64
	}{
		{
			name:            "sliceSpansExactlyFourWeeks",
			minIdx:          3,
			maxIdx:          31,
			slotIdx:         0,
			bucketsPerCycle: 7,
			want:            4,
		},
		{
			name:            "sliceSpansThreeWeeksAndFraction",
			minIdx:          3,
			maxIdx:          26,
			slotIdx:         0,
			bucketsPerCycle: 7,
			want:            3,
		},
		{
			name:            "sliceStartsOnTargetSlot",
			minIdx:          0,
			maxIdx:          21,
			slotIdx:         0,
			bucketsPerCycle: 7,
			want:            4,
		},
		{
			name:            "zeroCycles",
			minIdx:          1,
			maxIdx:          1,
			slotIdx:         0,
			bucketsPerCycle: 7,
			want:            0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := countExpectedCycles(test.minIdx, test.maxIdx, test.slotIdx, test.bucketsPerCycle)
			if got != test.want {
				t.Errorf("countExpectedCycles(%d, %d, %d, %d) = %d, want %d",
					test.minIdx, test.maxIdx, test.slotIdx, test.bucketsPerCycle, got, test.want)
			}
		})
	}
}
