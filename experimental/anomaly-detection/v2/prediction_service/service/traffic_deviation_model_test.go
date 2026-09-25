package service

import (
	"reflect"
	"testing"

	findingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

func makeTrafficDeviationModelProto(normalTemplates []string, anomalyScores map[string]float64) *modelpb.Model {
	pb := &modelpb.Model{}
	tpm := &modelpb.TrafficDeviationModel{}
	tpm.SetNormalTemplates(normalTemplates)
	tpm.SetAnomalyScores(anomalyScores)
	scenario := &modelpb.TrafficDeviationScenario{}
	scenario.SetModelData(tpm)
	pb.SetTrafficDeviation(scenario)
	return pb
}

func mustNewTrafficDeviationModel(pb *modelpb.Model) Model {
	m, err := NewTrafficDeviationModel(pb)
	if err != nil {
		panic(err)
	}
	return m
}

func TestNewTrafficDeviationModel(t *testing.T) {
	tests := []struct {
		name       string
		pb         *modelpb.Model
		wantNormal map[string]struct{}
		wantScores map[string]float64
		wantErr    bool
	}{
		{
			name:       "valid_proto",
			pb:         makeTrafficDeviationModelProto([]string{"SELECT", "INSERT"}, map[string]float64{"DROP": 0.9}),
			wantNormal: map[string]struct{}{"SELECT": {}, "INSERT": {}},
			wantScores: map[string]float64{"DROP": 0.9},
		},
		{
			name:       "nil_proto",
			pb:         nil,
			wantNormal: map[string]struct{}{},
			wantScores: map[string]float64{},
		},
		{
			name:    "mismatched_scenario_no_scenario_set",
			pb:      &modelpb.Model{}, // no scenario set
			wantErr: true,
		},
		{
			name: "mismatched_volumetric_spike_scenario",
			pb: func() *modelpb.Model {
				p := &modelpb.Model{}
				p.SetVolumetricSpike(&modelpb.VolumetricSpikeScenario{})
				return p
			}(),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, err := NewTrafficDeviationModel(test.pb)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewTrafficDeviationModel() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if m == nil {
				t.Fatalf("NewTrafficDeviationModel() returned nil model")
			}
			if !reflect.DeepEqual(test.wantNormal, m.normalTemplates) {
				t.Errorf("normalTemplates mismatch: got %v, want %v", m.normalTemplates, test.wantNormal)
			}
			if !reflect.DeepEqual(test.wantScores, m.anomalyScores) {
				t.Errorf("anomalyScores mismatch: got %v, want %v", m.anomalyScores, test.wantScores)
			}
		})
	}
}

func TestTrafficDeviationModel_Predict(t *testing.T) {
	entryNormal := &auditpb.AuditLogEntry{}
	entryNormal.SetNormalizedQueryTemplate("SELECT * FROM users WHERE id = %v")
	entryNormal.SetQueryStatement("SELECT * FROM users WHERE id = 12")

	entryAnomaly := &auditpb.AuditLogEntry{}
	entryAnomaly.SetNormalizedQueryTemplate("DROP TABLE users")
	entryAnomaly.SetQueryStatement("DROP TABLE users")

	entryZeroDay := &auditpb.AuditLogEntry{}
	entryZeroDay.SetNormalizedQueryTemplate("INSERT INTO logs (message) VALUES (%v)")
	entryZeroDay.SetQueryStatement("INSERT INTO logs (message) VALUES ('test')")

	tests := []struct {
		name        string
		model       Model
		logEntry    *auditpb.AuditLogEntry
		wantAnomaly bool
		wantScore   float64
	}{
		{
			name: "normal_template_match",
			model: mustNewTrafficDeviationModel(makeTrafficDeviationModelProto(
				[]string{"SELECT * FROM users WHERE id = %v"},
				map[string]float64{"DROP TABLE users": 0.5},
			)),
			logEntry:    entryNormal,
			wantAnomaly: false,
			wantScore:   1.0,
		},
		{
			name: "anomaly_template_match",
			model: mustNewTrafficDeviationModel(makeTrafficDeviationModelProto(
				[]string{"SELECT * FROM users WHERE id = %v"},
				map[string]float64{"DROP TABLE users": 0.5},
			)),
			logEntry:    entryAnomaly,
			wantAnomaly: true,
			wantScore:   0.5,
		},
		{
			name: "zero_day_anomaly",
			model: mustNewTrafficDeviationModel(makeTrafficDeviationModelProto(
				[]string{"SELECT * FROM users WHERE id = %v"},
				map[string]float64{"DROP TABLE users": 0.5},
			)),
			logEntry:    entryZeroDay,
			wantAnomaly: true,
			wantScore:   1.0,
		},
		{
			name:        "empty_model",
			model:       mustNewTrafficDeviationModel(nil),
			logEntry:    entryNormal,
			wantAnomaly: true,
			wantScore:   1.0,
		},
		{
			name:        "nil_log_entry",
			model:       mustNewTrafficDeviationModel(nil),
			logEntry:    nil,
			wantAnomaly: false,
			wantScore:   0.0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finding := test.model.Predict(test.logEntry)
			if finding.GetIsAnomalousPredict() != test.wantAnomaly {
				t.Errorf("Predict() got isAnomaly = %t, want %t", finding.GetIsAnomalousPredict(), test.wantAnomaly)
			}
			if finding.GetConfidenceScore() != test.wantScore {
				t.Errorf("Predict() got score = %f, want %f", finding.GetConfidenceScore(), test.wantScore)
			}
		})
	}
}

func TestGenerateAnomalyFinding(t *testing.T) {
	tests := []struct {
		name            string
		logEntry        *auditpb.AuditLogEntry
		isAnomaly       bool
		confidenceScore float64
		want            *findingpb.AnomalyFinding
	}{
		{
			name:            "nil_log_entry_returns_empty_finding",
			logEntry:        nil,
			isAnomaly:       false,
			confidenceScore: 1.0,
			want:            &findingpb.AnomalyFinding{},
		},
		{
			name: "valid_log_entry_populates_finding_fields",
			logEntry: func() *auditpb.AuditLogEntry {
				entry := &auditpb.AuditLogEntry{}
				entry.SetDbUser("db_user")
				entry.SetDbName("db_name")
				entry.SetQueryStatement("SELECT 1")
				entry.SetNormalizedQueryTemplate("SELECT %v")
				return entry
			}(),
			isAnomaly:       true,
			confidenceScore: 0.8,
			want: func() *findingpb.AnomalyFinding {
				finding := &findingpb.AnomalyFinding{}
				finding.SetDbUser("db_user")
				finding.SetDbName("db_name")
				finding.SetQueryStatement("SELECT 1")
				finding.SetNormalizedQueryTemplate("SELECT %v")
				finding.SetIsAnomalousPredict(true)
				finding.SetConfidenceScore(0.8)
				return finding
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := generateAnomalyFinding(test.logEntry, test.isAnomaly, test.confidenceScore)
			if got.GetDbUser() != test.want.GetDbUser() ||
				got.GetDbName() != test.want.GetDbName() ||
				got.GetQueryStatement() != test.want.GetQueryStatement() ||
				got.GetNormalizedQueryTemplate() != test.want.GetNormalizedQueryTemplate() ||
				got.GetIsAnomalousPredict() != test.want.GetIsAnomalousPredict() ||
				got.GetConfidenceScore() != test.want.GetConfidenceScore() {
				t.Errorf("generateAnomalyFinding() = %+v, want %+v", got, test.want)
			}
		})
	}
}
