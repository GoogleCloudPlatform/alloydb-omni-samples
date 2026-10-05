package service

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
	"google.golang.org/protobuf/types/known/durationpb"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

func makeVolumetricSpikeModelProto(baselines map[common.CycleBucketKey]VolumetricBaseline, bucketDuration, cycleDuration time.Duration) *modelpb.Model {
	pb := &modelpb.Model{}
	vsm := &modelpb.VolumetricSpikeModel{}
	var entries []*modelpb.VolumetricBaselineEntry
	for k, v := range baselines {
		key := &modelpb.CycleBucketKey{}
		key.SetDbUser(k.DbUser)
		key.SetQueryTemplate(k.QueryTemplate)
		key.SetBucketIndex(k.BucketIndex)

		val := &modelpb.VolumetricBaseline{}
		val.SetMean(v.Mean)
		val.SetStdDev(v.StdDev)
		val.SetUpperThreshold(v.UpperThreshold)
		val.SetDeviationMargin(v.DeviationMargin)

		entry := &modelpb.VolumetricBaselineEntry{}
		entry.SetKey(key)
		entry.SetValue(val)
		entries = append(entries, entry)
	}
	vsm.SetEntries(entries)
	if bucketDuration > 0 || cycleDuration > 0 {
		cc := &modelpb.CycleConfig{}
		cc.SetBucketDuration(durationpb.New(bucketDuration))
		cc.SetCycleDuration(durationpb.New(cycleDuration))
		if bucketDuration > 0 && cycleDuration > 0 {
			bucketsPerCycle, _ := common.ValidateDurations(bucketDuration, cycleDuration)
			cc.SetBucketsPerCycle(bucketsPerCycle)
		}
		vsm.SetCycleConfig(cc)
	}
	scenario := &modelpb.VolumetricSpikeScenario{}
	scenario.SetModelData(vsm)
	pb.SetVolumetricSpike(scenario)
	return pb
}

func mustNewVolumetricSpikeModel(pb *modelpb.Model) *VolumetricSpikeModel {
	m, err := NewVolumetricSpikeModel(pb)
	if err != nil {
		panic(err)
	}
	return m
}

func TestNewVolumetricSpikeModel(t *testing.T) {
	wantBaselines := map[common.CycleBucketKey]VolumetricBaseline{
		{DbUser: "user1", QueryTemplate: "t1", BucketIndex: 0}: {Mean: 1.0, StdDev: 0.1, UpperThreshold: 1.2, DeviationMargin: 0.2},
	}

	tests := []struct {
		name          string
		pb            *modelpb.Model
		wantBaselines map[common.CycleBucketKey]VolumetricBaseline
		wantBucketDur time.Duration
		wantCycleDur  time.Duration
		wantErr       bool
	}{
		{
			name:          "valid_proto",
			pb:            makeVolumetricSpikeModelProto(wantBaselines, 5*time.Minute, 1*time.Hour),
			wantBaselines: wantBaselines,
			wantBucketDur: 5 * time.Minute,
			wantCycleDur:  1 * time.Hour,
		},
		{
			name:          "nil_proto",
			pb:            nil,
			wantBaselines: map[common.CycleBucketKey]VolumetricBaseline{},
			wantBucketDur: 0,
			wantCycleDur:  0,
		},
		{
			name:    "mismatched_scenario_no_scenario_set",
			pb:      &modelpb.Model{}, // no scenario set
			wantErr: true,
		},
		{
			name: "mismatched_traffic_deviation_scenario",
			pb: func() *modelpb.Model {
				p := &modelpb.Model{}
				p.SetTrafficDeviation(&modelpb.TrafficDeviationScenario{})
				return p
			}(),
			wantErr: true,
		},
		{
			name: "nil_entry_key_or_value_skipped",
			pb: func() *modelpb.Model {
				p := &modelpb.Model{}
				vsm := &modelpb.VolumetricSpikeModel{}
				val := &modelpb.VolumetricBaseline{}
				val.SetMean(1.0)
				e1 := &modelpb.VolumetricBaselineEntry{}
				e1.SetValue(val)
				k2 := &modelpb.CycleBucketKey{}
				k2.SetDbUser("u1")
				e2 := &modelpb.VolumetricBaselineEntry{}
				e2.SetKey(k2)
				vsm.SetEntries([]*modelpb.VolumetricBaselineEntry{e1, e2})
				sc := &modelpb.VolumetricSpikeScenario{}
				sc.SetModelData(vsm)
				p.SetVolumetricSpike(sc)
				return p
			}(),
			wantBaselines: map[common.CycleBucketKey]VolumetricBaseline{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, err := NewVolumetricSpikeModel(test.pb)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewVolumetricSpikeModel() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if m == nil {
				t.Fatalf("NewVolumetricSpikeModel() returned nil model")
			}
			if !reflect.DeepEqual(test.wantBaselines, m.baselines) {
				t.Errorf("Baselines mismatch: got %v, want %v", m.baselines, test.wantBaselines)
			}
			if m.bucketDuration != test.wantBucketDur {
				t.Errorf("BucketDuration got %v, want %v", m.bucketDuration, test.wantBucketDur)
			}
			if m.cycleDuration != test.wantCycleDur {
				t.Errorf("CycleDuration got %v, want %v", m.cycleDuration, test.wantCycleDur)
			}
		})
	}
}

func TestVolumetricSpikeModel_Predict(t *testing.T) {
	entryNormal := &auditpb.AuditLogEntry{}
	entryNormal.SetDbUser("alice")
	entryNormal.SetNormalizedQueryTemplate("SELECT")
	entryNormal.SetTimestampMs(0)

	t.Run("normalRequest", func(t *testing.T) {
		baselines := map[common.CycleBucketKey]VolumetricBaseline{
			{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0}: {
				Mean:            10.0,
				StdDev:          2.0,
				UpperThreshold:  14.0,
				DeviationMargin: 4.0,
			},
		}
		pb := makeVolumetricSpikeModelProto(baselines, 60*time.Minute, 7*24*time.Hour)
		model := mustNewVolumetricSpikeModel(pb)

		finding := model.Predict(entryNormal)
		if finding.GetIsAnomalousPredict() {
			t.Errorf("Predict() got isAnomalous = true, want false")
		}
		if got, want := finding.GetConfidenceScore(), 1.0; got != want {
			t.Errorf("Predict() confidence score got %f, want %f", got, want)
		}
	})

	t.Run("anomalousRequestExceedingThreshold", func(t *testing.T) {
		baselines := map[common.CycleBucketKey]VolumetricBaseline{
			{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0}: {
				Mean:            10.0,
				StdDev:          2.0,
				UpperThreshold:  14.0,
				DeviationMargin: 4.0,
			},
		}
		pb := makeVolumetricSpikeModelProto(baselines, 60*time.Minute, 7*24*time.Hour)
		model := mustNewVolumetricSpikeModel(pb)

		// Send 14 normal requests (below or equal to threshold 14.0).
		for i := 0; i < 14; i++ {
			model.Predict(entryNormal)
		}

		// 15th request exceeds UpperThreshold of 14.0, triggering anomaly.
		findingAnomaly := model.Predict(entryNormal)
		if !findingAnomaly.GetIsAnomalousPredict() {
			t.Errorf("Predict() got isAnomalous = false, want true")
		}
		wantConf := 0.2
		if got := findingAnomaly.GetConfidenceScore(); math.Abs(got-wantConf) > 1e-9 {
			t.Errorf("Predict() confidence score got %f, want %f", got, wantConf)
		}
	})

	t.Run("zeroDayNoBaseline", func(t *testing.T) {
		pb := makeVolumetricSpikeModelProto(nil, 60*time.Minute, 7*24*time.Hour)
		model := mustNewVolumetricSpikeModel(pb)

		entryNoBaseline := &auditpb.AuditLogEntry{}
		entryNoBaseline.SetDbUser("bob")
		entryNoBaseline.SetNormalizedQueryTemplate("SELECT")
		entryNoBaseline.SetTimestampMs(0)

		finding := model.Predict(entryNoBaseline)
		if !finding.GetIsAnomalousPredict() {
			t.Errorf("Predict() got isAnomalous = false, want true")
		}
		if got, want := finding.GetConfidenceScore(), 1.0; got != want {
			t.Errorf("Predict() confidence score got %f, want %f", got, want)
		}
	})

	t.Run("zeroVarianceAnomaly", func(t *testing.T) {
		baselines := map[common.CycleBucketKey]VolumetricBaseline{
			{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0}: {
				Mean:            10.0,
				StdDev:          0.0,
				UpperThreshold:  10.0,
				DeviationMargin: 0.0,
			},
		}
		pb := makeVolumetricSpikeModelProto(baselines, 60*time.Minute, 7*24*time.Hour)
		model := mustNewVolumetricSpikeModel(pb)

		for i := 0; i < 10; i++ {
			finding := model.Predict(entryNormal)
			if finding.GetIsAnomalousPredict() {
				t.Errorf("Predict() request %d (zero variance) got isAnomalous = true, want false", i+1)
			}
		}

		findingAnomaly := model.Predict(entryNormal)
		if !findingAnomaly.GetIsAnomalousPredict() {
			t.Errorf("Predict() 11th request (zero variance) got isAnomalous = false, want true")
		}
	})

	t.Run("nilLogEntry", func(t *testing.T) {
		model := mustNewVolumetricSpikeModel(nil)
		finding := model.Predict(nil)
		if finding.GetIsAnomalousPredict() {
			t.Errorf("Predict(nil) got isAnomalous = true, want false")
		}
		if finding.GetConfidenceScore() != 0.0 {
			t.Errorf("Predict(nil) confidence score got %f, want 0.0", finding.GetConfidenceScore())
		}
	})

	t.Run("uninitializedLiveCounts", func(t *testing.T) {
		model := &VolumetricSpikeModel{
			bucketDuration:   5 * time.Minute,
			bucketsPerCycle:  12,
			liveBucketCounts: nil,
		}
		finding := model.Predict(entryNormal)
		if !finding.GetIsAnomalousPredict() {
			t.Errorf("Predict() on uninitialized model got isAnomalous = false, want true")
		}
	})
}

func TestVolumetricSpikeModel_ResetLiveCounts(t *testing.T) {
	baselines := map[common.CycleBucketKey]VolumetricBaseline{
		{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 0}: {
			Mean:            10.0,
			StdDev:          2.0,
			UpperThreshold:  14.0,
			DeviationMargin: 4.0,
		},
	}
	pb := makeVolumetricSpikeModelProto(baselines, 60*time.Minute, 7*24*time.Hour)
	model := mustNewVolumetricSpikeModel(pb)

	entry := &auditpb.AuditLogEntry{}
	entry.SetDbUser("alice")
	entry.SetNormalizedQueryTemplate("SELECT")
	entry.SetTimestampMs(0)

	model.Predict(entry)

	features, err := common.Extract(entry)
	if err != nil {
		t.Fatalf("Extract() failed: %v", err)
	}
	key := common.GetAbsoluteBucketKey(features, model.bucketDuration)
	if model.liveBucketCounts[key] != 1 {
		t.Fatalf("live count got %d, want 1", model.liveBucketCounts[key])
	}

	model.ResetLiveCounts()

	if len(model.liveBucketCounts) != 0 {
		t.Errorf("liveBucketCounts length after Reset got %d, want 0", len(model.liveBucketCounts))
	}
}
