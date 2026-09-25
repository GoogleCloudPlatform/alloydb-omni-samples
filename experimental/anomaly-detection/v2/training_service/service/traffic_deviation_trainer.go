// Package service implements trainers for anomaly detection models.
package service

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// ErrInvalidArgument indicates that an input argument is invalid.
var ErrInvalidArgument = errors.New("invalid argument")

// Trainer trains anomaly detection models by aggregating extracted log features.
type Trainer interface {
	Count(feature common.ExtractedLogFeatures)
	BuildModel() *modelpb.Model
}

// TrafficDeviationTrainer trains a model based on query template probability and anomaly threshold ratio.
type TrafficDeviationTrainer struct {
	anomalyThresholdRatio float64
	queryTemplateCounts   map[string]int
	totalLogCount         int

	minTimestampMs int64
	maxTimestampMs int64
}

// NewTrafficDeviationTrainer returns a new trainer with a validated threshold.
func NewTrafficDeviationTrainer(anomalyThresholdRatio float64) (*TrafficDeviationTrainer, error) {
	if anomalyThresholdRatio <= 0.0 || anomalyThresholdRatio >= 1.0 {
		return nil, fmt.Errorf("%w: anomalyThresholdRatio must be between 0.0 and 1.0 (exclusive)", ErrInvalidArgument)
	}
	return &TrafficDeviationTrainer{
		anomalyThresholdRatio: anomalyThresholdRatio,
		queryTemplateCounts:   make(map[string]int),
		minTimestampMs:        math.MaxInt64,
		maxTimestampMs:        math.MinInt64,
	}, nil
}

// Count counts query template occurrences.
func (t *TrafficDeviationTrainer) Count(feature common.ExtractedLogFeatures) {
	if feature.TimestampMs < t.minTimestampMs {
		t.minTimestampMs = feature.TimestampMs
	}
	if feature.TimestampMs > t.maxTimestampMs {
		t.maxTimestampMs = feature.TimestampMs
	}
	if t.queryTemplateCounts == nil {
		t.queryTemplateCounts = make(map[string]int)
	}
	t.queryTemplateCounts[feature.QueryTemplate]++
	t.totalLogCount++
}

// BuildModel builds Model containing normal traffic query templates
// and anomaly scores for anomalous templates.
func (t *TrafficDeviationTrainer) BuildModel() *modelpb.Model {
	tpm := &modelpb.TrafficDeviationModel{}
	config := &modelpb.TrafficDeviationTrainingConfig{}
	config.SetAnomalyThresholdRatio(t.anomalyThresholdRatio)

	scenario := &modelpb.TrafficDeviationScenario{}
	scenario.SetModelData(tpm)
	scenario.SetConfig(config)

	mp := &modelpb.Model{}
	mp.SetTrafficDeviation(scenario)

	metadata := &modelpb.ModelMetadata{}
	metadata.SetTrainingTime(timestamppb.Now())
	if t.totalLogCount > 0 {
		metadata.SetDataStartTime(timestamppb.New(time.UnixMilli(t.minTimestampMs)))
		metadata.SetDataEndTime(timestamppb.New(time.UnixMilli(t.maxTimestampMs)))
		metadata.SetTotalLogEntriesProcessed(int64(t.totalLogCount))
	}
	mp.SetMetadata(metadata)

	if t.totalLogCount == 0 {
		return mp
	}

	normalTemplates := make([]string, 0, len(t.queryTemplateCounts))
	anomalyScores := make(map[string]float64, len(t.queryTemplateCounts))

	for template, count := range t.queryTemplateCounts {
		prob := float64(count) / float64(t.totalLogCount)
		if prob >= t.anomalyThresholdRatio {
			normalTemplates = append(normalTemplates, template)
		} else {
			// Pre-calculate anomaly confidence scores to minimize online inference latency.
			score := 1.0 - (prob / t.anomalyThresholdRatio)
			anomalyScores[template] = score
		}
	}
	tpm.SetNormalTemplates(normalTemplates)
	tpm.SetAnomalyScores(anomalyScores)

	return mp
}
