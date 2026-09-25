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

// VolumetricBaseline stores the calculated Mean, StdDev, and the max request threshold.
type VolumetricBaseline struct {
	Mean   float64
	StdDev float64

	// UpperThreshold = Mean + DeviationMargin
	UpperThreshold float64

	// DeviationMargin = stdDevMultiplier * StdDev
	DeviationMargin float64
}

// VolumetricSpikeTrainer trains a model by calculating request counts baseline for periodic slots.
type VolumetricSpikeTrainer struct {
	stdDevMultiplier float64

	// bucketDuration is the duration of an individual time slot (e.g., 60 minutes).
	bucketDuration time.Duration

	// cycleDuration is the total duration of a repeating cycle including all time slots (e.g., 7 days for a weekly period).
	cycleDuration time.Duration

	// bucketsPerCycle is the number of discrete time slots contained in one repeat period (calculated as cycleDuration / bucketDuration, e.g., 168 slots in a weekly period).
	bucketsPerCycle     int64
	bucketRequestCounts map[common.AbsoluteBucketKey]int

	totalLogEntriesProcessed int64
	minTimestampMs           int64
	maxTimestampMs           int64
}

// NewVolumetricSpikeTrainer returns a trainer with validated stdDevMultiplier, bucketDuration, and cycleDuration.
func NewVolumetricSpikeTrainer(stdDevMultiplier float64, bucketDuration time.Duration, cycleDuration time.Duration) (*VolumetricSpikeTrainer, error) {
	if stdDevMultiplier <= 0.0 {
		return nil, fmt.Errorf("%w: stdDevMultiplier must be positive", ErrInvalidArgument)
	}

	bucketsPerCycle, err := common.ValidateDurations(bucketDuration, cycleDuration)
	if err != nil {
		return nil, err
	}

	return &VolumetricSpikeTrainer{
		stdDevMultiplier:    stdDevMultiplier,
		bucketDuration:      bucketDuration,
		cycleDuration:       cycleDuration,
		bucketsPerCycle:     bucketsPerCycle,
		bucketRequestCounts: make(map[common.AbsoluteBucketKey]int),
		minTimestampMs:      math.MaxInt64,
		maxTimestampMs:      math.MinInt64,
	}, nil
}

// Count accumulates request counts per absolute time bucket.
// The current method does not support concurrent calls.
func (t *VolumetricSpikeTrainer) Count(feature common.ExtractedLogFeatures) {
	t.totalLogEntriesProcessed++
	if feature.TimestampMs < t.minTimestampMs {
		t.minTimestampMs = feature.TimestampMs
	}
	if feature.TimestampMs > t.maxTimestampMs {
		t.maxTimestampMs = feature.TimestampMs
	}
	key := common.GetAbsoluteBucketKey(feature, t.bucketDuration)
	t.bucketRequestCounts[key]++
}

// BuildModel builds Model containing periodic slot's request count baselines.
func (t *VolumetricSpikeTrainer) BuildModel() *modelpb.Model {
	prbm := &modelpb.VolumetricSpikeModel{}
	cc := &modelpb.CycleConfig{}
	cc.SetBucketDuration(durationpb.New(t.bucketDuration))
	cc.SetCycleDuration(durationpb.New(t.cycleDuration))
	cc.SetBucketsPerCycle(t.bucketsPerCycle)
	prbm.SetCycleConfig(cc)

	config := &modelpb.VolumetricSpikeTrainingConfig{}
	config.SetStdDevMultiplier(t.stdDevMultiplier)

	scenario := &modelpb.VolumetricSpikeScenario{}
	scenario.SetModelData(prbm)
	scenario.SetConfig(config)

	mp := &modelpb.Model{}
	mp.SetVolumetricSpike(scenario)

	metadata := &modelpb.ModelMetadata{}
	metadata.SetTrainingTime(timestamppb.Now())
	if t.totalLogEntriesProcessed > 0 {
		metadata.SetDataStartTime(timestamppb.New(time.UnixMilli(t.minTimestampMs)))
		metadata.SetDataEndTime(timestamppb.New(time.UnixMilli(t.maxTimestampMs)))
		metadata.SetTotalLogEntriesProcessed(t.totalLogEntriesProcessed)
	}
	mp.SetMetadata(metadata)

	if len(t.bucketRequestCounts) == 0 {
		return mp
	}

	var entries []*modelpb.VolumetricBaselineEntry
	for slotKey, accumulator := range t.buildPeriodicAccumulators() {
		rb := accumulator.toVolumetricBaseline(t.stdDevMultiplier)

		protoKey := &modelpb.CycleBucketKey{}
		protoKey.SetDbUser(slotKey.DbUser)
		protoKey.SetQueryTemplate(slotKey.QueryTemplate)
		protoKey.SetBucketIndex(slotKey.BucketIndex)

		protoVal := &modelpb.VolumetricBaseline{}
		protoVal.SetMean(rb.Mean)
		protoVal.SetStdDev(rb.StdDev)
		protoVal.SetUpperThreshold(rb.UpperThreshold)
		protoVal.SetDeviationMargin(rb.DeviationMargin)

		entry := &modelpb.VolumetricBaselineEntry{}
		entry.SetKey(protoKey)
		entry.SetValue(protoVal)

		entries = append(entries, entry)
	}
	prbm.SetEntries(entries)

	return mp
}

func (t *VolumetricSpikeTrainer) buildPeriodicAccumulators() map[common.CycleBucketKey]welfordAccumulator {
	periodicAccumulators := make(map[common.CycleBucketKey]welfordAccumulator)
	minIdx, maxIdx := int64(math.MaxInt64), int64(math.MinInt64)

	// Accumulate stats and track the absolute bucket index bounds in one loop.
	for k, count := range t.bucketRequestCounts {
		if k.AbsoluteBucketIndex < minIdx {
			minIdx = k.AbsoluteBucketIndex
		}
		if k.AbsoluteBucketIndex > maxIdx {
			maxIdx = k.AbsoluteBucketIndex
		}

		slotKey := k.ToCycleBucketKey(t.bucketsPerCycle)
		wa := periodicAccumulators[slotKey]
		wa.update(float64(count))
		periodicAccumulators[slotKey] = wa
	}

	// Patch missing cycles with 0.0 based on real observed slot expected occurrences.
	for slotKey, wa := range periodicAccumulators {
		expectedWeeks := countExpectedCycles(minIdx, maxIdx, slotKey.BucketIndex, t.bucketsPerCycle)
		for i := int64(wa.totalCyclesCounted); i < expectedWeeks; i++ {
			wa.update(0.0)
		}
		periodicAccumulators[slotKey] = wa
	}

	return periodicAccumulators
}

// countExpectedCycles returns how many times slotIdx occurs in range [minIdx, maxIdx].
// E.g., Sunday occurs 3 times in a 25-day slice starting Wednesday.
func countExpectedCycles(minIdx, maxIdx, slotIdx, totalSlotsPerCycle int64) int64 {
	count := (maxIdx - minIdx) / totalSlotsPerCycle
	remStart := minIdx + count*totalSlotsPerCycle
	for idx := remStart; idx <= maxIdx; idx++ {
		if idx%totalSlotsPerCycle == slotIdx {
			count++
			break
		}
	}
	return count
}

// welfordAccumulator accumulates training statistics streamingly.
type welfordAccumulator struct {
	totalCyclesCounted           float64
	meanRequestsPerCycle         float64
	runningSquaredDifferencesSum float64
}

func (wa *welfordAccumulator) update(val float64) {
	wa.totalCyclesCounted++
	delta := val - wa.meanRequestsPerCycle
	wa.meanRequestsPerCycle += delta / wa.totalCyclesCounted
	delta2 := val - wa.meanRequestsPerCycle
	wa.runningSquaredDifferencesSum += delta * delta2
}

// toVolumetricBaseline converts the Welford accumulator to a VolumetricBaseline.
func (wa welfordAccumulator) toVolumetricBaseline(stdDevMultiplier float64) VolumetricBaseline {
	variance := 0.0
	if wa.totalCyclesCounted > 0 {
		variance = wa.runningSquaredDifferencesSum / wa.totalCyclesCounted
	}
	stdDev := math.Sqrt(variance)
	deviationMargin := stdDevMultiplier * stdDev

	return VolumetricBaseline{
		Mean:            wa.meanRequestsPerCycle,
		StdDev:          stdDev,
		UpperThreshold:  wa.meanRequestsPerCycle + deviationMargin,
		DeviationMargin: deviationMargin,
	}
}
