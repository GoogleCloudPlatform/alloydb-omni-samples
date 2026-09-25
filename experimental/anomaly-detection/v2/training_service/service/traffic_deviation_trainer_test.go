package service

import (
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"

	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

func TestNewTrafficDeviationTrainer(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		wantErr   bool
	}{
		{
			name:      "valid_threshold",
			threshold: 0.5,
			wantErr:   false,
		},
		{
			name:      "zero_threshold",
			threshold: 0.0,
			wantErr:   true,
		},
		{
			name:      "negative_threshold",
			threshold: -0.1,
			wantErr:   true,
		},
		{
			name:      "threshold_boundary_one",
			threshold: 1.0,
			wantErr:   true,
		},
		{
			name:      "exceeded_one_threshold",
			threshold: 1.5,
			wantErr:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTrafficDeviationTrainer(test.threshold)
			if (err != nil) != test.wantErr {
				t.Errorf("NewTrafficDeviationTrainer(%v) error = %v, wantErr %v", test.threshold, err, test.wantErr)
			}
		})
	}
}

func TestTrafficDeviationTrainer_Count(t *testing.T) {
	tests := []struct {
		name          string
		features      []common.ExtractedLogFeatures
		wantCount     map[string]int
		wantTotal     int
		uninitialized bool
	}{
		{
			name: "single_feature",
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT"},
			},
			wantCount: map[string]int{"SELECT": 1},
			wantTotal: 1,
		},
		{
			name: "multiple_same_and_different_features",
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT"},
				{QueryTemplate: "SELECT"},
				{QueryTemplate: "DROP"},
			},
			wantCount: map[string]int{"SELECT": 2, "DROP": 1},
			wantTotal: 3,
		},
		{
			name: "empty_template",
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: ""},
			},
			wantCount: map[string]int{"": 1},
			wantTotal: 1,
		},
		{
			name: "uninitialized_map",
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT"},
			},
			wantCount:     map[string]int{"SELECT": 1},
			wantTotal:     1,
			uninitialized: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var trainer *TrafficDeviationTrainer
			if test.uninitialized {
				trainer = &TrafficDeviationTrainer{anomalyThresholdRatio: 0.5}
			} else {
				var err error
				trainer, err = NewTrafficDeviationTrainer(0.5)
				if err != nil {
					t.Fatalf("failed to initialize trainer: %v", err)
				}
			}

			for _, f := range test.features {
				trainer.Count(f)
			}

			if trainer.totalLogCount != test.wantTotal {
				t.Errorf("totalLogCount got %d, want %d", trainer.totalLogCount, test.wantTotal)
			}

			if len(trainer.queryTemplateCounts) != len(test.wantCount) {
				t.Errorf("queryTemplateCounts map size got %d, want %d", len(trainer.queryTemplateCounts), len(test.wantCount))
			}
			for k, wantVal := range test.wantCount {
				gotVal, exists := trainer.queryTemplateCounts[k]
				if !exists {
					t.Errorf("expected queryTemplateCounts to contain %q", k)
				} else if gotVal != wantVal {
					t.Errorf("count for %q got %d, want %d", k, gotVal, wantVal)
				}
			}
		})
	}
}

func TestTrafficDeviationTrainer_BuildModel(t *testing.T) {
	tests := []struct {
		name             string
		threshold        float64
		features         []common.ExtractedLogFeatures
		wantNormal       map[string]struct{}
		wantAnomaly      map[string]float64
		wantMinTimestamp int64
		wantMaxTimestamp int64
	}{
		{
			name:             "no_logs_processed",
			threshold:        0.5,
			features:         nil,
			wantNormal:       map[string]struct{}{},
			wantAnomaly:      map[string]float64{},
			wantMinTimestamp: 0,
			wantMaxTimestamp: 0,
		},
		{
			name:      "mix_normal_and_anomalous",
			threshold: 0.5,
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT", TimestampMs: 1000},
				{QueryTemplate: "SELECT", TimestampMs: 2000},
				{QueryTemplate: "SELECT", TimestampMs: 3000},
				{QueryTemplate: "SELECT", TimestampMs: 4000},
				{QueryTemplate: "DROP", TimestampMs: 5000},
			}, // 80% SELECT, 20% DROP
			wantNormal:       map[string]struct{}{"SELECT": struct{}{}},
			wantAnomaly:      map[string]float64{"DROP": 0.6}, // 1 - (0.2 / 0.5) = 0.6
			wantMinTimestamp: 1000,
			wantMaxTimestamp: 5000,
		},
		{
			name:      "all_normal",
			threshold: 0.1,
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT", TimestampMs: 1000},
				{QueryTemplate: "INSERT", TimestampMs: 2000},
			}, // 50% SELECT, 50% INSERT
			wantNormal:       map[string]struct{}{"SELECT": struct{}{}, "INSERT": struct{}{}},
			wantAnomaly:      map[string]float64{},
			wantMinTimestamp: 1000,
			wantMaxTimestamp: 2000,
		},
		{
			name:      "all_anomalous",
			threshold: 0.9,
			features: []common.ExtractedLogFeatures{
				{QueryTemplate: "SELECT", TimestampMs: 1000},
				{QueryTemplate: "INSERT", TimestampMs: 2000},
			}, // 50% SELECT, 50% INSERT
			wantNormal:       map[string]struct{}{},
			wantAnomaly:      map[string]float64{"SELECT": 1.0 - (0.5 / 0.9), "INSERT": 1.0 - (0.5 / 0.9)},
			wantMinTimestamp: 1000,
			wantMaxTimestamp: 2000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trainer, err := NewTrafficDeviationTrainer(test.threshold)
			if err != nil {
				t.Fatalf("failed to initialize trainer: %v", err)
			}
			for _, f := range test.features {
				trainer.Count(f)
			}

			var model *modelpb.Model = trainer.BuildModel()
			if model == nil {
				t.Fatalf("BuildModel() returned nil")
			}

			if !model.HasTrafficDeviation() {
				t.Fatalf("BuildModel() did not return TrafficDeviation case")
			}
			probModel := model.GetTrafficDeviation().GetModelData()

			gotNormalTemplates := probModel.GetNormalTemplates()
			if len(gotNormalTemplates) != len(test.wantNormal) {
				t.Errorf("normalTemplates count got %d, want %d", len(gotNormalTemplates), len(test.wantNormal))
			}
			for _, template := range gotNormalTemplates {
				if _, exists := test.wantNormal[template]; !exists {
					t.Errorf("unexpected normal template %q in proto", template)
				}
			}

			gotAnomalyScores := probModel.GetAnomalyScores()
			if len(gotAnomalyScores) != len(test.wantAnomaly) {
				t.Errorf("anomalyScores count got %d, want %d", len(gotAnomalyScores), len(test.wantAnomaly))
			}
			for k, wantScore := range test.wantAnomaly {
				gotScore, exists := gotAnomalyScores[k]
				if !exists {
					t.Errorf("expected anomalyScores to contain %q", k)
				} else if gotScore != wantScore {
					t.Errorf("score for %q got %f, want %f", k, gotScore, wantScore)
				}
			}

			// Verify Metadata
			if !model.HasMetadata() {
				t.Fatalf("expected model to have metadata")
			}
			metadata := model.GetMetadata()
			if metadata.GetTrainingTime() == nil {
				t.Errorf("expected TrainingTime to be set")
			} else {
				// Verify training time is close to now (within 10 seconds)
				dt := time.Since(metadata.GetTrainingTime().AsTime())
				if dt < 0 || dt > 10*time.Second {
					t.Errorf("TrainingTime %v is not recent (delta: %v)", metadata.GetTrainingTime().AsTime(), dt)
				}
			}

			if len(test.features) > 0 {
				if metadata.GetTotalLogEntriesProcessed() != int64(len(test.features)) {
					t.Errorf("TotalLogEntriesProcessed got %d, want %d", metadata.GetTotalLogEntriesProcessed(), len(test.features))
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
				if model.GetTrafficDeviation().GetConfig().GetAnomalyThresholdRatio() != test.threshold {
					t.Errorf("AnomalyThresholdRatio got %f, want %f", model.GetTrafficDeviation().GetConfig().GetAnomalyThresholdRatio(), test.threshold)
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
