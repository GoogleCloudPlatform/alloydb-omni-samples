package service

import (
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"

	findingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// VolumetricBaseline stores the calculated Mean, StdDev, and the max request threshold.
type VolumetricBaseline struct {
	Mean            float64
	StdDev          float64
	UpperThreshold  float64
	DeviationMargin float64
}

// VolumetricSpikeModel represents the periodic request baseline model.
type VolumetricSpikeModel struct {
	baselines        map[common.CycleBucketKey]VolumetricBaseline
	bucketDuration   time.Duration
	cycleDuration    time.Duration
	bucketsPerCycle  int64
	liveBucketCounts map[common.AbsoluteBucketKey]int
}

// NewVolumetricSpikeModel constructs the model from proto.
func NewVolumetricSpikeModel(pb *modelpb.Model) (*VolumetricSpikeModel, error) {
	baselines := make(map[common.CycleBucketKey]VolumetricBaseline)
	var bucketDuration, cycleDuration time.Duration
	var bucketsPerCycle int64

	// If the pb is nil, a new empty model is created.
	if pb == nil {
		return &VolumetricSpikeModel{
			baselines:        baselines,
			bucketDuration:   bucketDuration,
			cycleDuration:    cycleDuration,
			bucketsPerCycle:  bucketsPerCycle,
			liveBucketCounts: make(map[common.AbsoluteBucketKey]int),
		}, nil
	}

	// If the pb has no volumetric spike scenario, an error is returned.
	if !pb.HasVolumetricSpike() {
		return nil, fmt.Errorf("invalid model scenario: expected VolumetricSpike, got %v", pb.WhichScenario())
	}

	// If the pb has valid volumetric spike model data, a new deep copy model is created.
	vsm := pb.GetVolumetricSpike().GetModelData()
	if vsm != nil {
		for _, entry := range vsm.GetEntries() {
			k := entry.GetKey()
			v := entry.GetValue()
			if k == nil || v == nil {
				continue
			}
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
			baselines[key] = val
		}
		bucketDuration = vsm.GetCycleConfig().GetBucketDuration().AsDuration()
		cycleDuration = vsm.GetCycleConfig().GetCycleDuration().AsDuration()
		bucketsPerCycle, _ = common.ValidateDurations(bucketDuration, cycleDuration)
	}

	return &VolumetricSpikeModel{
		baselines:        baselines,
		bucketDuration:   bucketDuration,
		cycleDuration:    cycleDuration,
		bucketsPerCycle:  bucketsPerCycle,
		liveBucketCounts: make(map[common.AbsoluteBucketKey]int),
	}, nil
}

// Predict performs anomaly detection using periodic slot baselines.
func (m *VolumetricSpikeModel) Predict(logEntry *auditpb.AuditLogEntry) *findingpb.AnomalyFinding {
	// Increment and retrieve the live bucket counts.
	features, err := common.Extract(logEntry)
	if err != nil {
		return &findingpb.AnomalyFinding{}
	}
	bucketKey := common.GetAbsoluteBucketKey(features, m.bucketDuration)
	if m.liveBucketCounts == nil {
		m.liveBucketCounts = make(map[common.AbsoluteBucketKey]int)
	}
	m.liveBucketCounts[bucketKey]++
	actualCount := m.liveBucketCounts[bucketKey]

	slotKey := bucketKey.ToCycleBucketKey(m.bucketsPerCycle)
	baseline, ok := m.baselines[slotKey]
	if !ok {
		return generateAnomalyFinding(logEntry, true, 1.0)
	}

	// Flag as anomaly if actual count exceeds threshold, using > to avoid flagging zero-variance cases.
	isAnomaly := float64(actualCount) > baseline.UpperThreshold
	if !isAnomaly {
		return generateAnomalyFinding(logEntry, false, 1.0)
	}

	// Calculate confidence score for the detected anomaly.
	confidenceScore := 1.0
	if baseline.DeviationMargin > 0 {
		actualDiff := float64(actualCount) - baseline.Mean
		confidenceScore = 1.0 - (baseline.DeviationMargin / actualDiff)
	}
	return generateAnomalyFinding(logEntry, true, confidenceScore)
}

// ResetLiveCounts clears all recorded live bucket counts.
func (m *VolumetricSpikeModel) ResetLiveCounts() {
	m.liveBucketCounts = make(map[common.AbsoluteBucketKey]int)
}
