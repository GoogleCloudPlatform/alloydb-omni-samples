package service

import (
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"

	findingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// TemporalDeviationModel represents the silent bucket anomaly detection model.
type TemporalDeviationModel struct {
	silentSlots     map[common.CycleBucketKey]float64
	bucketDuration  time.Duration
	cycleDuration   time.Duration
	bucketsPerCycle int64
}

// NewTemporalDeviationModel constructs the model from proto.
func NewTemporalDeviationModel(pb *modelpb.Model) (*TemporalDeviationModel, error) {
	silentSlots := make(map[common.CycleBucketKey]float64)
	var bucketDuration, cycleDuration time.Duration
	var bucketsPerCycle int64

	// If the pb is nil, a new empty model is created.
	if pb == nil {
		return &TemporalDeviationModel{
			silentSlots:     silentSlots,
			bucketDuration:  bucketDuration,
			cycleDuration:   cycleDuration,
			bucketsPerCycle: bucketsPerCycle,
		}, nil
	}

	// If the pb has no temporal deviation scenario, an error is returned.
	if pb.GetTemporalDeviation() == nil {
		return nil, fmt.Errorf("invalid model scenario: expected TemporalDeviation, got %T", pb.GetScenario())
	}

	// If the pb has valid temporal deviation model data, a new deep copy model is created.
	tdm := pb.GetTemporalDeviation().GetModelData()
	if tdm != nil {
		for _, entry := range tdm.GetEntries() {
			k := entry.GetKey()
			if k == nil {
				continue
			}
			key := common.CycleBucketKey{
				DbUser:        k.GetDbUser(),
				QueryTemplate: k.GetQueryTemplate(),
				BucketIndex:   k.GetBucketIndex(),
			}
			silentSlots[key] = entry.GetConfidenceScore()
		}
		bucketDuration = tdm.GetCycleConfig().GetBucketDuration().AsDuration()
		cycleDuration = tdm.GetCycleConfig().GetCycleDuration().AsDuration()
		bucketsPerCycle, _ = common.ValidateDurations(bucketDuration, cycleDuration)
	}

	return &TemporalDeviationModel{
		silentSlots:     silentSlots,
		bucketDuration:  bucketDuration,
		cycleDuration:   cycleDuration,
		bucketsPerCycle: bucketsPerCycle,
	}, nil
}

// Predict performs anomaly detection by checking if the log entry falls into a silent bucket.
func (m *TemporalDeviationModel) Predict(logEntry *auditpb.AuditLogEntry) *findingpb.AnomalyFinding {
	features, err := common.Extract(logEntry)
	if err != nil {
		return &findingpb.AnomalyFinding{}
	}
	bucketKey := common.GetAbsoluteBucketKey(features, m.bucketDuration)
	slotKey := bucketKey.ToCycleBucketKey(m.bucketsPerCycle)

	confidenceScore, isSilent := m.silentSlots[slotKey]
	if !isSilent {
		return generateAnomalyFinding(logEntry, false, 1.0)
	}
	return generateAnomalyFinding(logEntry, true, confidenceScore)
}
