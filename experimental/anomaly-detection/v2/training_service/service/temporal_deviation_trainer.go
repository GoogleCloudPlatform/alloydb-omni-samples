package service

import (
	"fmt"
	"math"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// TemporalDeviationTrainer trains a model by identifying silent periodic slots.
type TemporalDeviationTrainer struct {
	silentThresholdRatio float64

	// bucketDuration is the duration of an individual time slot (e.g., 60 minutes).
	bucketDuration time.Duration

	// cycleDuration is the total duration of a repeating cycle including all time slots (e.g., 7 days for a weekly period).
	cycleDuration time.Duration

	// bucketsPerCycle is the number of discrete time slots contained in one repeat period (calculated as cycleDuration / bucketDuration, e.g., 168 slots in a weekly period).
	bucketsPerCycle int64

	cycleBucketRequestCounts map[common.CycleBucketKey]int
	userTemplateTotalCounts  map[userTemplateKey]int

	totalLogEntriesProcessed int64
	minTimestampMs           int64
	maxTimestampMs           int64
}

type userTemplateKey struct {
	DbUser        string
	QueryTemplate string
}

// NewTemporalDeviationTrainer returns a new TemporalDeviationTrainer with validated inputs.
func NewTemporalDeviationTrainer(silentThresholdRatio float64, bucketDuration time.Duration, cycleDuration time.Duration) (*TemporalDeviationTrainer, error) {
	if silentThresholdRatio <= 0.0 || silentThresholdRatio >= 1.0 {
		return nil, fmt.Errorf("%w: silentThresholdRatio must be between 0.0 and 1.0 (exclusive)", ErrInvalidArgument)
	}
	bucketsPerCycle, err := common.ValidateDurations(bucketDuration, cycleDuration)
	if err != nil {
		return nil, err
	}
	return &TemporalDeviationTrainer{
		silentThresholdRatio:     silentThresholdRatio,
		bucketDuration:           bucketDuration,
		cycleDuration:            cycleDuration,
		bucketsPerCycle:          bucketsPerCycle,
		cycleBucketRequestCounts: make(map[common.CycleBucketKey]int),
		userTemplateTotalCounts:  make(map[userTemplateKey]int),
		minTimestampMs:           math.MaxInt64,
		maxTimestampMs:           math.MinInt64,
	}, nil
}

// Count increments request counts per periodic slot and total counts per user-template.
// The current method does not support concurrent calls.
func (t *TemporalDeviationTrainer) Count(feature common.ExtractedLogFeatures) {
	t.totalLogEntriesProcessed++
	if feature.TimestampMs < t.minTimestampMs {
		t.minTimestampMs = feature.TimestampMs
	}
	if feature.TimestampMs > t.maxTimestampMs {
		t.maxTimestampMs = feature.TimestampMs
	}
	bucketKey := common.GetAbsoluteBucketKey(feature, t.bucketDuration)
	slotKey := bucketKey.ToCycleBucketKey(t.bucketsPerCycle)
	t.cycleBucketRequestCounts[slotKey]++

	utKey := userTemplateKey{
		DbUser:        feature.DbUser,
		QueryTemplate: feature.QueryTemplate,
	}
	t.userTemplateTotalCounts[utKey]++
}

// BuildModel precalculates and stores a Model containing silent periodic slots and their anomaly confidence scores.
func (t *TemporalDeviationTrainer) BuildModel() *modelpb.Model {
	sbm := &modelpb.TemporalDeviationModel{}
	cc := &modelpb.CycleConfig{}
	cc.SetBucketDuration(durationpb.New(t.bucketDuration))
	cc.SetCycleDuration(durationpb.New(t.cycleDuration))
	cc.SetBucketsPerCycle(t.bucketsPerCycle)
	sbm.SetCycleConfig(cc)

	config := &modelpb.TemporalDeviationTrainingConfig{}
	config.SetSilentThresholdRatio(t.silentThresholdRatio)

	scenario := &modelpb.TemporalDeviationScenario{}
	scenario.SetModelData(sbm)
	scenario.SetConfig(config)

	mp := &modelpb.Model{}
	mp.SetTemporalDeviation(scenario)

	metadata := &modelpb.ModelMetadata{}
	metadata.SetTrainingTime(timestamppb.Now())
	if t.totalLogEntriesProcessed > 0 {
		metadata.SetDataStartTime(timestamppb.New(time.UnixMilli(t.minTimestampMs)))
		metadata.SetDataEndTime(timestamppb.New(time.UnixMilli(t.maxTimestampMs)))
		metadata.SetTotalLogEntriesProcessed(t.totalLogEntriesProcessed)
	}
	mp.SetMetadata(metadata)

	if len(t.cycleBucketRequestCounts) == 0 {
		return mp
	}

	var entries []*modelpb.TemporalSilentBucketEntry
	for utKey, totalCount := range t.userTemplateTotalCounts {
		if totalCount == 0 {
			continue
		}

		silentThresholdCount := float64(totalCount) * t.silentThresholdRatio

		// Iterate all slots to capture zero-count silent buckets.
		for slotIdx := int64(0); slotIdx < t.bucketsPerCycle; slotIdx++ {
			slotKey := common.CycleBucketKey{
				DbUser:        utKey.DbUser,
				QueryTemplate: utKey.QueryTemplate,
				BucketIndex:   slotIdx,
			}

			count := float64(t.cycleBucketRequestCounts[slotKey])

			// Exclude boundary cases (count == threshold) to avoid storing slots with 0.0 confidence.
			if count < silentThresholdCount {
				confidence := 1.0 - (count / silentThresholdCount)

				protoKey := &modelpb.CycleBucketKey{}
				protoKey.SetDbUser(slotKey.DbUser)
				protoKey.SetQueryTemplate(slotKey.QueryTemplate)
				protoKey.SetBucketIndex(slotKey.BucketIndex)

				entry := &modelpb.TemporalSilentBucketEntry{}
				entry.SetKey(protoKey)
				entry.SetConfidenceScore(confidence)

				entries = append(entries, entry)
			}
		}
	}
	sbm.SetEntries(entries)

	return mp
}
