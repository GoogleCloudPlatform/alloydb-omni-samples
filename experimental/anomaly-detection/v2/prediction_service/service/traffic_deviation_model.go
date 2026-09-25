// Package service implements prediction models for anomaly detection.
package service

import (
	"fmt"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"

	findingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// Model predicts and generates an AnomalyFinding for a given log entry.
type Model interface {
	Predict(logEntry *auditpb.AuditLogEntry) *findingpb.AnomalyFinding
}

// TrafficDeviationModel represents the query template probability model.
type TrafficDeviationModel struct {
	normalTemplates map[string]struct{}
	anomalyScores   map[string]float64
}

// NewTrafficDeviationModel constructs the model from proto.
func NewTrafficDeviationModel(pb *modelpb.Model) (*TrafficDeviationModel, error) {
	normalTemplates := make(map[string]struct{})
	anomalyScores := make(map[string]float64)

	// If the pb is nil, a new empty model is created.
	if pb == nil {
		return &TrafficDeviationModel{
			normalTemplates: normalTemplates,
			anomalyScores:   anomalyScores,
		}, nil
	}

	// If the pb has no traffic deviation scenario, an error is returned.
	if !pb.HasTrafficDeviation() {
		return nil, fmt.Errorf("invalid model scenario: expected TrafficDeviation, got %v", pb.WhichScenario())
	}

	// If the pb has valid traffic deviation model data, a new deep copy model is created.
	tpm := pb.GetTrafficDeviation().GetModelData()
	if tpm != nil {
		for _, t := range tpm.GetNormalTemplates() {
			normalTemplates[t] = struct{}{}
		}
		for k, v := range tpm.GetAnomalyScores() {
			anomalyScores[k] = v
		}
	}

	return &TrafficDeviationModel{
		normalTemplates: normalTemplates,
		anomalyScores:   anomalyScores,
	}, nil
}

// Predict classifies logEntry as normal or anomalous.
func (m *TrafficDeviationModel) Predict(logEntry *auditpb.AuditLogEntry) *findingpb.AnomalyFinding {
	features, err := common.Extract(logEntry)
	if err != nil {
		return &findingpb.AnomalyFinding{}
	}
	queryTemplate := features.QueryTemplate
	if _, normal := m.normalTemplates[queryTemplate]; normal {
		return generateAnomalyFinding(logEntry, false, 1.0)
	}
	score := 1.0
	if s, anomalous := m.anomalyScores[queryTemplate]; anomalous {
		score = s
	}
	return generateAnomalyFinding(logEntry, true, score)
}

func generateAnomalyFinding(logEntry *auditpb.AuditLogEntry, isAnomaly bool, confidenceScore float64) *findingpb.AnomalyFinding {
	finding := &findingpb.AnomalyFinding{}
	if logEntry == nil {
		return finding
	}
	finding.SetDbUser(logEntry.GetDbUser())
	finding.SetDbName(logEntry.GetDbName())
	finding.SetQueryStatement(logEntry.GetQueryStatement())
	finding.SetNormalizedQueryTemplate(logEntry.GetNormalizedQueryTemplate())
	finding.SetIsAnomalousPredict(isAnomaly)
	finding.SetConfidenceScore(confidenceScore)
	return finding
}
