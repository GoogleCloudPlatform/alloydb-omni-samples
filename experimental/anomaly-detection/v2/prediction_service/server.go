// Package main implements the gRPC service for AlloyDB anomaly detection prediction.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	findingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	predictiongrpc "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/proto"
	predictionpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/proto"
)

var (
	port      = flag.String("port", "50052", "The server port")
	modelsDir = flag.String("models_dir", "", "Directory where models are stored")
)

type predictionServer struct {
	predictiongrpc.UnimplementedPredictionServiceServer
}

// NewPredictionServer creates a new predictionServer.
func NewPredictionServer() predictiongrpc.PredictionServiceServer {
	return &predictionServer{}
}

func validatePredictRequest(req *predictionpb.PredictRequest) error {
	if req == nil {
		return errors.New("request is nil")
	}
	switch req.WhichPayload() {
	case predictionpb.PredictRequest_SingleLog_case:
		if req.GetSingleLog() == nil {
			return errors.New("single_log cannot be nil")
		}
	case predictionpb.PredictRequest_BatchLogs_case:
		if req.GetBatchLogs() == nil || len(req.GetBatchLogs().GetLogEntries()) == 0 {
			return errors.New("batch_logs must contain at least one log entry")
		}
	case predictionpb.PredictRequest_PredictLogPath_case:
		if req.GetPredictLogPath() == "" {
			return errors.New("predict_log_path cannot be empty")
		}
	default:
		return errors.New("payload is required")
	}
	return nil
}

func validatePushModelRequest(req *predictionpb.PushModelRequest) error {
	if req == nil {
		return errors.New("request is nil")
	}
	if req.GetModelLocation() == "" {
		return errors.New("model_location cannot be empty")
	}
	if req.GetModel() == nil {
		return errors.New("model is required")
	}
	return nil
}

// Predict predicts anomaly status and confidence score across selected models(by location or filter)
// over target log payloads(single log, batch logs or file stream).
func (s *predictionServer) Predict(ctx context.Context, req *predictionpb.PredictRequest) (*predictionpb.PredictResponse, error) {
	// Validate request
	if err := validatePredictRequest(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	items, err := findMatchingModels(ctx, req.GetModelLocation(), req.GetFilter(), true /* isPredict */)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "no matching models found for prediction")
	}

	var models []service.Model
	for _, item := range items {
		m, err := modelFromProto(item.proto)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to initialize model from %q: %v", item.entry.GetModelLocation(), err)
		}
		models = append(models, m)
	}

	// Perform prediction based on payload
	if singleLog := req.GetSingleLog(); singleLog != nil {
		return predictSingle(ctx, models, singleLog)
	}

	if batchLogs := req.GetBatchLogs(); batchLogs != nil {
		return predictBatch(ctx, models, batchLogs)
	}

	return predictFile(ctx, models, req.GetPredictLogPath())
}

// PushModel registers and saves a newly trained model within the prediction service.
func (s *predictionServer) PushModel(ctx context.Context, req *predictionpb.PushModelRequest) (*predictionpb.PushModelResponse, error) {
	// Validate request
	if err := validatePushModelRequest(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	if *modelsDir == "" {
		return nil, status.Error(codes.FailedPrecondition, "models_dir flag is not set")
	}

	modelLocation := filepath.Join(resolvePath(*modelsDir), filepath.Base(req.GetModelLocation()))
	modelProto := req.GetModel()

	// Save model to disk
	if err := saveModelToDisk(ctx, modelLocation, modelProto); err != nil {
		log.Printf("Failed to save model to %q: %v", modelLocation, err)
		return nil, status.Errorf(codes.Internal, "failed to save model: %v", err)
	}

	log.Printf("Successfully pushed and saved model to %q", modelLocation)
	return &predictionpb.PushModelResponse{}, nil
}

// ListModels lists overview information(e.g. metadata, training_config, etc) for matching models based on location or filter.
func (s *predictionServer) ListModels(ctx context.Context, req *predictionpb.ListModelsRequest) (*predictionpb.ListModelsResponse, error) {
	items, err := findMatchingModels(ctx, req.GetModelLocation(), req.GetFilter(), false /* isPredict */)
	if err != nil {
		return nil, err
	}

	var entries []*predictionpb.ModelEntry
	for _, item := range items {
		entries = append(entries, item.entry)
	}

	resp := &predictionpb.ListModelsResponse{}
	resp.SetModels(entries)
	return resp, nil
}

func matchesModelFilter(filter *predictionpb.ModelFilter, entry *predictionpb.ModelEntry) bool {
	if filter == nil {
		return true
	}

	// 1. Metadata filter
	if v := filter.GetMetadataFilter().GetTrainerVersion(); v != "" {
		if entry.GetMetadata().GetTrainerVersion() != v {
			return false
		}
	}

	// 2. Scenario config filter
	switch filter.WhichScenarioConfigFilter() {
	case predictionpb.ModelFilter_TrafficDeviation_case:
		cfg := entry.GetTrafficDeviationConfig()
		if cfg == nil {
			return false
		}
		if filter := filter.GetTrafficDeviation(); filter.GetAnomalyThresholdRatio() != 0 {
			return cfg.GetAnomalyThresholdRatio() == filter.GetAnomalyThresholdRatio()
		}

	case predictionpb.ModelFilter_VolumetricSpike_case:
		cfg := entry.GetVolumetricSpikeConfig()
		if cfg == nil {
			return false
		}
		if filter := filter.GetVolumetricSpike(); filter.GetStdDevMultiplier() != 0 {
			return cfg.GetStdDevMultiplier() == filter.GetStdDevMultiplier()
		}

	case predictionpb.ModelFilter_TemporalDeviation_case:
		cfg := entry.GetTemporalDeviationConfig()
		if cfg == nil {
			return false
		}
		if filter := filter.GetTemporalDeviation(); filter.GetSilentThresholdRatio() != 0 {
			return cfg.GetSilentThresholdRatio() == filter.GetSilentThresholdRatio()
		}
	}

	return true
}

type loadedModelItem struct {
	proto *modelpb.Model
	entry *predictionpb.ModelEntry
}

func findMatchingModels(ctx context.Context, location string, filter *predictionpb.ModelFilter, isPredict bool) ([]*loadedModelItem, error) {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = *modelsDir
	}
	if loc == "" {
		return nil, status.Error(codes.FailedPrecondition, "models_dir flag is not set")
	}

	if !filepath.IsAbs(loc) && loc != *modelsDir && *modelsDir != "" {
		loc = filepath.Join(*modelsDir, loc)
	}
	targetPath := resolvePath(loc)

	// Case 1: Single model file
	if stat, err := os.Stat(targetPath); err == nil && !stat.IsDir() {
		return collectModels(ctx, []string{targetPath}, filter, isPredict)
	}

	// Case 2: Directory of models (custom dir or default models_dir)
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		if isPredict && loc != "" {
			return nil, status.Errorf(codes.FailedPrecondition, "failed to load model from %q: %v", targetPath, err)
		}
		return nil, status.Errorf(codes.Internal, "failed to list models directory %q: %v", targetPath, err)
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			paths = append(paths, filepath.Join(targetPath, entry.Name()))
		}
	}
	return collectModels(ctx, paths, filter, isPredict)
}

func collectModels(ctx context.Context, paths []string, filter *predictionpb.ModelFilter, isPredict bool) ([]*loadedModelItem, error) {
	var results []*loadedModelItem
	for _, p := range paths {
		modelProto, err := loadModelProtoFromDisk(ctx, p)
		if err != nil {
			if isPredict && len(paths) == 1 {
				log.Printf("Failed to load model from %q: %v", p, err)
				return nil, status.Errorf(codes.FailedPrecondition, "failed to load model from %q: %v", p, err)
			}
			log.Printf("Failed to load model from %q: %v", p, err)
			continue
		}
		entry := buildModelEntry(p, modelProto)
		if matchesModelFilter(filter, entry) {
			results = append(results, &loadedModelItem{proto: modelProto, entry: entry})
		}
	}
	return results, nil
}

func buildModelEntry(path string, modelProto *modelpb.Model) *predictionpb.ModelEntry {
	entry := &predictionpb.ModelEntry{}
	entry.SetModelLocation(path)
	entry.SetMetadata(modelProto.GetMetadata())

	switch modelProto.WhichScenario() {
	case modelpb.Model_TrafficDeviation_case:
		entry.SetTrafficDeviationConfig(modelProto.GetTrafficDeviation().GetConfig())
	case modelpb.Model_VolumetricSpike_case:
		entry.SetVolumetricSpikeConfig(modelProto.GetVolumetricSpike().GetConfig())
	case modelpb.Model_TemporalDeviation_case:
		entry.SetTemporalDeviationConfig(modelProto.GetTemporalDeviation().GetConfig())
	}

	return entry
}

func resolvePath(path string) string {
	resolvedPath := path
	if !filepath.IsAbs(resolvedPath) {
		if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
			resolvedPath = filepath.Join(workspaceDir, resolvedPath)
		}
	}
	return resolvedPath
}

func loadModelProtoFromDisk(ctx context.Context, path string) (*modelpb.Model, error) {
	resolvedPath := resolvePath(path)
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read model file: %w", err)
	}
	pb := &modelpb.Model{}
	if err := proto.Unmarshal(data, pb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model proto: %w", err)
	}
	return pb, nil
}

func modelFromProto(pb *modelpb.Model) (service.Model, error) {
	if pb == nil {
		return nil, fmt.Errorf("model proto is nil")
	}
	switch pb.WhichScenario() {
	case modelpb.Model_TrafficDeviation_case:
		return service.NewTrafficDeviationModel(pb)

	case modelpb.Model_VolumetricSpike_case:
		return service.NewVolumetricSpikeModel(pb)

	case modelpb.Model_TemporalDeviation_case:
		return service.NewTemporalDeviationModel(pb)

	default:
		return nil, fmt.Errorf("unknown scenario case in proto: %v", pb.WhichScenario())
	}
}

func saveModelToDisk(ctx context.Context, path string, modelProto *modelpb.Model) error {
	resolvedPath := resolvePath(path)
	data, err := proto.Marshal(modelProto)
	if err != nil {
		return fmt.Errorf("failed to marshal model proto: %w", err)
	}
	if err := os.WriteFile(resolvedPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write model file: %w", err)
	}
	return nil
}

func resetModels(models []service.Model) {
	for _, m := range models {
		if r, ok := m.(interface{ ResetLiveCounts() }); ok {
			r.ResetLiveCounts()
		}
	}
}

func predictSingle(ctx context.Context, models []service.Model, singleLog *auditpb.AuditLogEntry) (*predictionpb.PredictResponse, error) {
	resp := &predictionpb.PredictResponse{}
	if len(models) == 1 {
		resp.SetSingleFinding(models[0].Predict(singleLog))
		return resp, nil
	}
	var findings []*findingpb.AnomalyFinding
	for _, m := range models {
		findings = append(findings, m.Predict(singleLog))
	}
	batchResp := &predictionpb.AnomalyFindingBatch{}
	batchResp.SetFindings(findings)
	resp.SetBatchFindings(batchResp)
	return resp, nil
}

func predictBatch(ctx context.Context, models []service.Model, batchLogs *predictionpb.AuditLogBatch) (*predictionpb.PredictResponse, error) {
	resetModels(models)

	var findings []*findingpb.AnomalyFinding
	for _, m := range models {
		for _, entry := range batchLogs.GetLogEntries() {
			findings = append(findings, m.Predict(entry))
		}
	}
	batchResp := &predictionpb.AnomalyFindingBatch{}
	batchResp.SetFindings(findings)
	resp := &predictionpb.PredictResponse{}
	resp.SetBatchFindings(batchResp)
	return resp, nil
}

func predictFile(ctx context.Context, models []service.Model, path string) (*predictionpb.PredictResponse, error) {
	resolvedPath := resolvePath(path)
	resetModels(models)

	// Write findings to a temporary file
	outPath := filepath.Join(os.TempDir(), fmt.Sprintf("anomaly_findings_%d.textproto", time.Now().UnixNano()))
	outFile, err := os.Create(outPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create output file: %v", err)
	}
	defer outFile.Close()

	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	for _, m := range models {
		err = common.IngestBinProtoStream(ctx, resolvedPath, func(entry *auditpb.AuditLogEntry) error {
			data, err := prototext.Marshal(m.Predict(entry))
			if err != nil {
				return status.Errorf(codes.Internal, "failed to marshal finding to textproto: %v", err)
			}
			if _, err := writer.WriteString(string(data) + "\n"); err != nil {
				return status.Errorf(codes.Internal, "failed to write finding: %v", err)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, common.ErrInvalidArgument) {
				return nil, status.Errorf(codes.InvalidArgument, "failed to ingest and predict logs: %v", err)
			}
			return nil, status.Errorf(codes.Internal, "failed to ingest and predict logs: %v", err)
		}
	}

	resp := &predictionpb.PredictResponse{}
	resp.SetFindingsPath(outPath)
	return resp, nil
}

func main() {
	flag.Parse()

	lis, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		log.Fatalf("Failed to listen on port %s: %v", *port, err)
	}

	grpcServer := grpc.NewServer()
	srv := NewPredictionServer()
	predictiongrpc.RegisterPredictionServiceServer(grpcServer, srv)

	log.Printf("Starting gRPC PredictionServiceServer listening on :%s", *port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
