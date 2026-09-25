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

func makeTemporalDeviationModelProto(silentSlots map[common.CycleBucketKey]float64, bucketDuration, cycleDuration time.Duration) *modelpb.Model {
	pb := &modelpb.Model{}
	tdm := &modelpb.TemporalDeviationModel{}
	var entries []*modelpb.TemporalSilentBucketEntry
	for k, score := range silentSlots {
		key := &modelpb.CycleBucketKey{}
		key.SetDbUser(k.DbUser)
		key.SetQueryTemplate(k.QueryTemplate)
		key.SetBucketIndex(k.BucketIndex)

		entry := &modelpb.TemporalSilentBucketEntry{}
		entry.SetKey(key)
		entry.SetConfidenceScore(score)
		entries = append(entries, entry)
	}
	tdm.SetEntries(entries)
	if bucketDuration > 0 || cycleDuration > 0 {
		cc := &modelpb.CycleConfig{}
		cc.SetBucketDuration(durationpb.New(bucketDuration))
		cc.SetCycleDuration(durationpb.New(cycleDuration))
		if bucketDuration > 0 && cycleDuration > 0 {
			bucketsPerCycle, _ := common.ValidateDurations(bucketDuration, cycleDuration)
			cc.SetBucketsPerCycle(bucketsPerCycle)
		}
		tdm.SetCycleConfig(cc)
	}
	scenario := &modelpb.TemporalDeviationScenario{}
	scenario.SetModelData(tdm)
	pb.SetTemporalDeviation(scenario)
	return pb
}

func mustNewTemporalDeviationModel(pb *modelpb.Model) *TemporalDeviationModel {
	m, err := NewTemporalDeviationModel(pb)
	if err != nil {
		panic(err)
	}
	return m
}

func TestNewTemporalDeviationModel(t *testing.T) {
	wantSilentSlots := map[common.CycleBucketKey]float64{
		{DbUser: "alice", QueryTemplate: "SELECT", BucketIndex: 1}: 0.8,
	}

	tests := []struct {
		name            string
		pb              *modelpb.Model
		wantSilentSlots map[common.CycleBucketKey]float64
		wantBucketDur   time.Duration
		wantCycleDur    time.Duration
		wantBuckets     int64
		wantErr         bool
	}{
		{
			name:            "valid_proto",
			pb:              makeTemporalDeviationModelProto(wantSilentSlots, 10*time.Minute, 1*time.Hour),
			wantSilentSlots: wantSilentSlots,
			wantBucketDur:   10 * time.Minute,
			wantCycleDur:    1 * time.Hour,
			wantBuckets:     6,
		},
		{
			name:            "nil_proto",
			pb:              nil,
			wantSilentSlots: map[common.CycleBucketKey]float64{},
			wantBucketDur:   0,
			wantCycleDur:    0,
			wantBuckets:     0,
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
			name: "nil_entry_key_skipped",
			pb: func() *modelpb.Model {
				p := &modelpb.Model{}
				tdm := &modelpb.TemporalDeviationModel{}
				e := &modelpb.TemporalSilentBucketEntry{}
				e.SetConfidenceScore(0.8)
				tdm.SetEntries([]*modelpb.TemporalSilentBucketEntry{e})
				sc := &modelpb.TemporalDeviationScenario{}
				sc.SetModelData(tdm)
				p.SetTemporalDeviation(sc)
				return p
			}(),
			wantSilentSlots: map[common.CycleBucketKey]float64{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, err := NewTemporalDeviationModel(test.pb)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewTemporalDeviationModel() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if m == nil {
				t.Fatalf("NewTemporalDeviationModel() returned nil model")
			}
			if !reflect.DeepEqual(test.wantSilentSlots, m.silentSlots) {
				t.Errorf("SilentSlots mismatch: got %v, want %v", m.silentSlots, test.wantSilentSlots)
			}
			if m.bucketDuration != test.wantBucketDur {
				t.Errorf("BucketDuration got %v, want %v", m.bucketDuration, test.wantBucketDur)
			}
			if m.cycleDuration != test.wantCycleDur {
				t.Errorf("CycleDuration got %v, want %v", m.cycleDuration, test.wantCycleDur)
			}
			if m.bucketsPerCycle != test.wantBuckets {
				t.Errorf("BucketsPerCycle got %d, want %d", m.bucketsPerCycle, test.wantBuckets)
			}
		})
	}
}

func TestTemporalDeviationModel_Predict(t *testing.T) {
	bucketDuration := 10 * time.Minute
	cycleDuration := 1 * time.Hour

	slot1Key := common.CycleBucketKey{
		DbUser:        "alice",
		QueryTemplate: "SELECT * FROM users",
		BucketIndex:   1,
	}

	silentSlots := map[common.CycleBucketKey]float64{
		slot1Key: 0.85,
	}

	entrySlot0 := &auditpb.AuditLogEntry{}
	entrySlot0.SetDbUser("alice")
	entrySlot0.SetNormalizedQueryTemplate("SELECT * FROM users")
	entrySlot0.SetTimestampMs(0)

	entrySlot1 := &auditpb.AuditLogEntry{}
	entrySlot1.SetDbUser("alice")
	entrySlot1.SetNormalizedQueryTemplate("SELECT * FROM users")
	entrySlot1.SetTimestampMs(10 * 60 * 1000)

	entryWrapped := &auditpb.AuditLogEntry{}
	entryWrapped.SetDbUser("alice")
	entryWrapped.SetNormalizedQueryTemplate("SELECT * FROM users")
	entryWrapped.SetTimestampMs(70 * 60 * 1000)

	entryDiffUser := &auditpb.AuditLogEntry{}
	entryDiffUser.SetDbUser("bob")
	entryDiffUser.SetNormalizedQueryTemplate("SELECT * FROM users")
	entryDiffUser.SetTimestampMs(10 * 60 * 1000)

	model := mustNewTemporalDeviationModel(makeTemporalDeviationModelProto(silentSlots, bucketDuration, cycleDuration))
	emptyModel := mustNewTemporalDeviationModel(makeTemporalDeviationModelProto(nil, bucketDuration, cycleDuration))

	tests := []struct {
		name        string
		model       *TemporalDeviationModel
		logEntry    *auditpb.AuditLogEntry
		wantAnomaly bool
		wantScore   float64
	}{
		{
			name:        "active_bucket_normal",
			model:       model,
			logEntry:    entrySlot0,
			wantAnomaly: false,
			wantScore:   1.0,
		},
		{
			name:        "silent_bucket_anomalous",
			model:       model,
			logEntry:    entrySlot1,
			wantAnomaly: true,
			wantScore:   0.85,
		},
		{
			name:        "nil_log_entry",
			model:       model,
			logEntry:    nil,
			wantAnomaly: false,
			wantScore:   0.0,
		},
		{
			name:        "empty_silent_slots_model",
			model:       emptyModel,
			logEntry:    entrySlot1,
			wantAnomaly: false,
			wantScore:   1.0,
		},
		{
			name:        "periodic_slot_wraparound",
			model:       model,
			logEntry:    entryWrapped,
			wantAnomaly: true,
			wantScore:   0.85,
		},
		{
			name:        "different_user_or_template",
			model:       model,
			logEntry:    entryDiffUser,
			wantAnomaly: false,
			wantScore:   1.0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.model.Predict(test.logEntry)
			if got.GetIsAnomalousPredict() != test.wantAnomaly {
				t.Errorf("Predict() got isAnomalous = %t, want %t", got.GetIsAnomalousPredict(), test.wantAnomaly)
			}
			if math.Abs(got.GetConfidenceScore()-test.wantScore) > 1e-9 {
				t.Errorf("Predict() got confidence score = %f, want %f", got.GetConfidenceScore(), test.wantScore)
			}
		})
	}
}
