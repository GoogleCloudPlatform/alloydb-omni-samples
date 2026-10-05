package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	predictionpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/proto"
)

// createTestLogFile creates a binary log file with a single AuditLogEntry.
func createTestLogFile(ctx context.Context, t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create test log file: %v", err)
	}
	defer file.Close()

	entry := &auditpb.AuditLogEntry{}
	entry.SetNormalizedQueryTemplate("SELECT")
	entry.SetQueryStatement("SELECT 1")
	entry.SetDbUser("db_user")
	entry.SetDbName("db_name")
	entry.SetTimestampMs(time.Now().UnixMilli())

	if _, err := protodelim.MarshalTo(file, entry); err != nil {
		t.Fatalf("failed to write test log entry: %v", err)
	}
}

// createTestModelFile serializes a Model proto and writes it to disk.
func createTestModelFile(ctx context.Context, t *testing.T, path string, modelProto *modelpb.Model) {
	t.Helper()
	data, err := proto.Marshal(modelProto)
	if err != nil {
		t.Fatalf("failed to marshal model proto: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write model file: %v", err)
	}
}

func makeSingleLogPredictRequest(entry *auditpb.AuditLogEntry, modelLocation string) *predictionpb.PredictRequest {
	req := &predictionpb.PredictRequest{}
	req.SetSingleLog(entry)
	req.SetModelLocation(modelLocation)
	return req
}

func makeBatchLogsPredictRequest(entries []*auditpb.AuditLogEntry, modelLocation string) *predictionpb.PredictRequest {
	req := &predictionpb.PredictRequest{}
	batch := &predictionpb.AuditLogBatch{}
	batch.SetLogEntries(entries)
	req.SetBatchLogs(batch)
	req.SetModelLocation(modelLocation)
	return req
}

func makeFilePredictRequest(path string, modelLocation string) *predictionpb.PredictRequest {
	req := &predictionpb.PredictRequest{}
	req.SetPredictLogPath(path)
	req.SetModelLocation(modelLocation)
	return req
}

func makeTestTrafficDeviationModel(ratio float64, version string) *modelpb.Model {
	tdProto := &modelpb.TrafficDeviationModel{}
	tdProto.SetNormalTemplates([]string{"SELECT"})
	tdProto.SetAnomalyScores(map[string]float64{"DROP": 0.5})

	tdConfig := &modelpb.TrafficDeviationTrainingConfig{}
	tdConfig.SetAnomalyThresholdRatio(ratio)

	tdScenario := &modelpb.TrafficDeviationScenario{}
	tdScenario.SetModelData(tdProto)
	tdScenario.SetConfig(tdConfig)

	modelProto := &modelpb.Model{}
	modelProto.SetTrafficDeviation(tdScenario)
	if version != "" {
		meta := &modelpb.ModelMetadata{}
		meta.SetTrainerVersion(version)
		modelProto.SetMetadata(meta)
	}
	return modelProto
}

func makeTestVolumetricSpikeModel(multiplier float64, version string) *modelpb.Model {
	vsProto := &modelpb.VolumetricSpikeModel{}
	key := &modelpb.CycleBucketKey{}
	key.SetDbUser("db_user")
	key.SetQueryTemplate("SELECT")
	key.SetBucketIndex(0)

	val := &modelpb.VolumetricBaseline{}
	val.SetMean(1.0)
	val.SetStdDev(1.0)
	val.SetUpperThreshold(1.5)

	entry := &modelpb.VolumetricBaselineEntry{}
	entry.SetKey(key)
	entry.SetValue(val)
	vsProto.SetEntries([]*modelpb.VolumetricBaselineEntry{entry})

	config := &modelpb.CycleConfig{}
	config.SetBucketDuration(durationpb.New(24 * time.Hour))
	config.SetCycleDuration(durationpb.New(24 * time.Hour))
	config.SetBucketsPerCycle(1)
	vsProto.SetCycleConfig(config)

	vsConfig := &modelpb.VolumetricSpikeTrainingConfig{}
	vsConfig.SetStdDevMultiplier(multiplier)

	vsScenario := &modelpb.VolumetricSpikeScenario{}
	vsScenario.SetModelData(vsProto)
	vsScenario.SetConfig(vsConfig)

	modelProto := &modelpb.Model{}
	modelProto.SetVolumetricSpike(vsScenario)
	if version != "" {
		meta := &modelpb.ModelMetadata{}
		meta.SetTrainerVersion(version)
		modelProto.SetMetadata(meta)
	}
	return modelProto
}

func TestPredict(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	inputPath := filepath.Join(tmpDir, "predict_input.binproto")
	createTestLogFile(ctx, t, inputPath)

	// 1. Create a valid TrafficDeviationModel proto for testing disk loading
	modelProto := makeTestTrafficDeviationModel(0.1, "")
	diskModelPath := filepath.Join(tmpDir, "model_td.bin")
	createTestModelFile(ctx, t, diskModelPath, modelProto)

	// 2. Create a valid VolumetricSpikeModel proto for testing disk loading
	vsModelProto := makeTestVolumetricSpikeModel(3.0, "")
	vsDiskModelPath := filepath.Join(tmpDir, "model_vs.bin")
	createTestModelFile(ctx, t, vsDiskModelPath, vsModelProto)

	// 3. Create a corrupt model file (invalid proto bytes)
	corruptModelPath := filepath.Join(tmpDir, "corrupt_model.bin")
	if err := os.WriteFile(corruptModelPath, []byte("invalid proto bytes"), 0644); err != nil {
		t.Fatalf("failed to create corrupt model file: %v", err)
	}

	// 4. Create an uninitializable model file in a separate subfolder (valid Model proto, but missing scenario case)
	invalidSubDir := t.TempDir()
	uninitializableModelPath := filepath.Join(invalidSubDir, "uninitializable_model.bin")
	invalidScenarioModel := &modelpb.Model{} // No scenario set
	createTestModelFile(ctx, t, uninitializableModelPath, invalidScenarioModel)

	// 5. Create a corrupt input log file (invalid binproto stream)
	corruptInputPath := filepath.Join(tmpDir, "corrupt_input.binproto")
	if err := os.WriteFile(corruptInputPath, []byte("not a valid binproto stream"), 0644); err != nil {
		t.Fatalf("failed to create corrupt input file: %v", err)
	}

	entryNormal := &auditpb.AuditLogEntry{}
	entryNormal.SetNormalizedQueryTemplate("SELECT")
	entryNormal.SetDbUser("db_user")
	entryNormal.SetDbName("db_name")
	entryNormal.SetTimestampMs(time.Now().UnixMilli())

	entryAnomaly := &auditpb.AuditLogEntry{}
	entryAnomaly.SetNormalizedQueryTemplate("DROP")
	entryAnomaly.SetDbUser("db_user")
	entryAnomaly.SetDbName("db_name")
	entryAnomaly.SetTimestampMs(time.Now().UnixMilli())

	tests := []struct {
		name     string
		req      *predictionpb.PredictRequest
		wantCode codes.Code
		before   func(t *testing.T) func()
		verify   func(t *testing.T, resp *predictionpb.PredictResponse)
	}{
		// Phase 1: Request Validation & Model Resolution
		{
			name: "invalid_request_empty_payload",
			req: func() *predictionpb.PredictRequest {
				r := &predictionpb.PredictRequest{}
				r.SetModelLocation(diskModelPath)
				return r
			}(),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "model_resolution_dir_not_set",
			req:      makeSingleLogPredictRequest(entryNormal, ""),
			wantCode: codes.FailedPrecondition,
			before: func(t *testing.T) func() {
				oldDir := *modelsDir
				*modelsDir = ""
				return func() {
					*modelsDir = oldDir
				}
			},
		},
		{
			name:     "model_resolution_model_not_found",
			req:      makeSingleLogPredictRequest(entryNormal, "non_existent_model_loc"),
			wantCode: codes.FailedPrecondition,
		},
		{
			name:     "model_resolution_corrupt_model_file",
			req:      makeSingleLogPredictRequest(entryNormal, corruptModelPath),
			wantCode: codes.FailedPrecondition,
		},
		{
			// Verifies internal error when model proto is parsed but modelFromProto fails initialization.
			name:     "model_initialization_error",
			req:      makeSingleLogPredictRequest(entryNormal, uninitializableModelPath),
			wantCode: codes.Internal,
		},

		// Phase 2: Single Log Payload Branch
		{
			name:     "happy_path_single_log_normal",
			req:      makeSingleLogPredictRequest(entryNormal, diskModelPath),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				finding := resp.GetSingleFinding()
				if finding == nil {
					t.Fatalf("expected single finding, got nil")
				}
				if finding.GetIsAnomalousPredict() {
					t.Errorf("expected isAnomalousPredict to be false for SELECT")
				}
				if finding.GetConfidenceScore() != 1.0 {
					t.Errorf("expected score 1.0, got %f", finding.GetConfidenceScore())
				}
			},
		},
		{
			name:     "happy_path_single_log_anomaly",
			req:      makeSingleLogPredictRequest(entryAnomaly, diskModelPath),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				finding := resp.GetSingleFinding()
				if finding == nil {
					t.Fatalf("expected single finding, got nil")
				}
				if !finding.GetIsAnomalousPredict() {
					t.Errorf("expected isAnomalousPredict to be true for DROP")
				}
				if finding.GetConfidenceScore() != 0.5 {
					t.Errorf("expected score 0.5, got %f", finding.GetConfidenceScore())
				}
			},
		},

		// Phase 3: Batch Logs Payload Branch
		{
			name:     "happy_path_batch_logs",
			req:      makeBatchLogsPredictRequest([]*auditpb.AuditLogEntry{entryNormal, entryAnomaly}, diskModelPath),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				batch := resp.GetBatchFindings()
				if batch == nil {
					t.Fatalf("expected batch findings, got nil")
				}
				findings := batch.GetFindings()
				if len(findings) != 2 {
					t.Fatalf("expected 2 findings, got %d", len(findings))
				}
				if findings[0].GetIsAnomalousPredict() {
					t.Errorf("first finding should be normal")
				}
				if !findings[1].GetIsAnomalousPredict() {
					t.Errorf("second finding should be anomalous")
				}
			},
		},
		{
			name:     "happy_path_volumetric_spike_batch_predict",
			req:      makeBatchLogsPredictRequest([]*auditpb.AuditLogEntry{entryNormal, entryNormal}, vsDiskModelPath),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				batch := resp.GetBatchFindings()
				if batch == nil {
					t.Fatalf("expected batch findings, got nil")
				}
				findings := batch.GetFindings()
				if len(findings) != 2 {
					t.Fatalf("expected 2 findings, got %d", len(findings))
				}
				if findings[0].GetIsAnomalousPredict() {
					t.Errorf("expected first batch finding to be normal, got anomalous")
				}
				if !findings[1].GetIsAnomalousPredict() {
					t.Errorf("expected second batch finding to be anomalous, got normal")
				}
			},
		},

		// Phase 4: File Stream Payload Branch
		{
			name:     "happy_path_predict_log_path",
			req:      makeFilePredictRequest(inputPath, diskModelPath),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				outPath := resp.GetFindingsPath()
				if outPath == "" {
					t.Fatalf("expected findings path, got empty")
				}
				if _, err := os.Stat(outPath); err != nil {
					t.Errorf("output findings file %q does not exist: %v", outPath, err)
				}
				t.Cleanup(func() {
					os.Remove(outPath)
				})
			},
		},
		{
			name:     "predict_log_path_corrupt_stream",
			req:      makeFilePredictRequest(corruptInputPath, diskModelPath),
			wantCode: codes.InvalidArgument,
		},

		// Phase 5: Multi-Model & Filtering
		{
			name:     "happy_path_empty_location_defaults_to_all_models",
			req:      makeSingleLogPredictRequest(entryNormal, ""),
			wantCode: codes.OK,
			before: func(t *testing.T) func() {
				oldDir := *modelsDir
				*modelsDir = tmpDir
				return func() {
					*modelsDir = oldDir
				}
			},
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				batch := resp.GetBatchFindings()
				if batch == nil {
					t.Fatalf("expected batch findings for all-model predict with empty location, got nil")
				}
				if len(batch.GetFindings()) == 0 {
					t.Errorf("expected non-empty findings for all-model predict")
				}
			},
		},
		{
			name: "happy_path_predict_filtered_scenario",
			req: func() *predictionpb.PredictRequest {
				r := makeSingleLogPredictRequest(entryNormal, "")
				filter := &predictionpb.ModelFilter{}
				filter.SetTrafficDeviation(&modelpb.TrafficDeviationTrainingConfig{})
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			before: func(t *testing.T) func() {
				oldDir := *modelsDir
				*modelsDir = tmpDir
				return func() {
					*modelsDir = oldDir
				}
			},
			verify: func(t *testing.T, resp *predictionpb.PredictResponse) {
				if resp.GetSingleFinding() == nil && resp.GetBatchFindings() == nil {
					t.Fatalf("expected finding response for filtered scenario predict")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "happy_path_predict_log_path" {
				t.Setenv("BUILD_WORKSPACE_DIRECTORY", tmpDir)
			}
			if test.before != nil {
				cleanup := test.before(t)
				if cleanup != nil {
					defer cleanup()
				}
			}
			s := &predictionServer{}
			resp, err := s.Predict(ctx, test.req)

			gotStatus, _ := status.FromError(err)
			if gotStatus.Code() != test.wantCode {
				t.Errorf("Predict() got code %v, want code %v, error: %v", gotStatus.Code(), test.wantCode, err)
			}

			if test.wantCode == codes.OK && test.verify != nil {
				test.verify(t, resp)
			}
		})
	}
}

func TestPushModel(t *testing.T) {
	ctx := context.Background()
	s := &predictionServer{}
	tmpDir := t.TempDir()

	oldModelsDir := *modelsDir
	*modelsDir = tmpDir
	defer func() { *modelsDir = oldModelsDir }()

	modelProto := makeTestTrafficDeviationModel(0.1, "")

	tests := []struct {
		name              string
		modelsDirOverride string
		req               *predictionpb.PushModelRequest
		wantCode          codes.Code
		verify            func(t *testing.T)
	}{
		// Phase 1: Request Validation & Flag Interception
		{
			name: "invalid_request_nil_model",
			req: func() *predictionpb.PushModelRequest {
				r := &predictionpb.PushModelRequest{}
				r.SetModelLocation("invalid_loc")
				return r
			}(),
			wantCode: codes.InvalidArgument,
		},
		{
			name:              "models_dir_not_set",
			modelsDirOverride: "UNSET",
			req: func() *predictionpb.PushModelRequest {
				r := &predictionpb.PushModelRequest{}
				r.SetModelLocation("pushed_model.bin")
				r.SetModel(modelProto)
				return r
			}(),
			wantCode: codes.FailedPrecondition,
		},

		// Phase 2: Disk Write Error
		{
			name:              "model_persistence_write_failure",
			modelsDirOverride: filepath.Join(tmpDir, "unwritable_file"),
			req: func() *predictionpb.PushModelRequest {
				unwritableFile := filepath.Join(tmpDir, "unwritable_file")
				if err := os.WriteFile(unwritableFile, []byte("data"), 0644); err != nil {
					t.Fatalf("failed to create file for write failure test: %v", err)
				}
				r := &predictionpb.PushModelRequest{}
				r.SetModelLocation("model.bin")
				r.SetModel(modelProto)
				return r
			}(),
			wantCode: codes.Internal,
		},

		// Phase 3: Happy Path Success
		{
			name: "happy_path_push_model_success",
			req: func() *predictionpb.PushModelRequest {
				r := &predictionpb.PushModelRequest{}
				r.SetModelLocation("pushed_model.bin")
				r.SetModel(modelProto)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T) {
				pushedModelPath := filepath.Join(tmpDir, "pushed_model.bin")
				if _, err := os.Stat(pushedModelPath); err != nil {
					t.Errorf("expected pushed model file %q to exist: %v", pushedModelPath, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.modelsDirOverride == "UNSET" {
				*modelsDir = ""
			} else if test.modelsDirOverride != "" {
				*modelsDir = test.modelsDirOverride
			} else {
				*modelsDir = tmpDir
			}
			_, err := s.PushModel(ctx, test.req)
			gotStatus, _ := status.FromError(err)
			if gotStatus.Code() != test.wantCode {
				t.Errorf("PushModel() got code %v, want %v, err: %v", gotStatus.Code(), test.wantCode, err)
			}
			if test.wantCode == codes.OK && test.verify != nil {
				test.verify(t)
			}
		})
	}
}

func TestListModels(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create test models
	modelTD := makeTestTrafficDeviationModel(0.1, "v1-td")
	modelVS := makeTestVolumetricSpikeModel(3.0, "v1-vs")

	pathTD := filepath.Join(tmpDir, "model_td.bin")
	pathVS := filepath.Join(tmpDir, "model_vs.bin")
	createTestModelFile(ctx, t, pathTD, modelTD)
	createTestModelFile(ctx, t, pathVS, modelVS)

	// Create a non-model file to ensure it's ignored
	nonModelPath := filepath.Join(tmpDir, "not_a_model.txt")
	if err := os.WriteFile(nonModelPath, []byte("some random data"), 0644); err != nil {
		t.Fatalf("failed to create non-model file: %v", err)
	}

	oldModelsDir := *modelsDir
	*modelsDir = tmpDir
	defer func() { *modelsDir = oldModelsDir }()

	s := &predictionServer{}

	tests := []struct {
		name     string
		req      *predictionpb.ListModelsRequest
		wantCode codes.Code
		before   func(t *testing.T) func()
		verify   func(t *testing.T, resp *predictionpb.ListModelsResponse)
	}{
		// Phase 1: Default Scanning & Location Lookup
		{
			name:     "happy_path_list_all_default_dir",
			req:      &predictionpb.ListModelsRequest{},
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 2 {
					t.Fatalf("expected 2 models for empty request, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "happy_path_list_all_whitespace_location",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation("   ")
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 2 {
					t.Fatalf("expected 2 models for whitespace location, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "happy_path_single_model_location_valid",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation(pathTD)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				models := resp.GetModels()
				if len(models) != 1 {
					t.Fatalf("expected 1 model for single location lookup, got %d", len(models))
				}
				if models[0].GetModelLocation() != pathTD {
					t.Errorf("expected location %s, got %s", pathTD, models[0].GetModelLocation())
				}
			},
		},
		{
			name: "happy_path_custom_directory_location",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation(tmpDir)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 2 {
					t.Fatalf("expected 2 models for custom directory location %s, got %d", tmpDir, len(resp.GetModels()))
				}
			},
		},
		{
			name: "single_model_location_nonexistent",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation("nonexistent_model.bin")
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 0 {
					t.Fatalf("expected 0 models for nonexistent file, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name:     "models_dir_not_set",
			req:      &predictionpb.ListModelsRequest{},
			wantCode: codes.FailedPrecondition,
			before: func(t *testing.T) func() {
				oldDir := *modelsDir
				*modelsDir = ""
				return func() {
					*modelsDir = oldDir
				}
			},
		},

		// Phase 2: Metadata & Scenario Filter Matching
		{
			name: "filter_by_scenario_matching",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				filter.SetTrafficDeviation(&modelpb.TrafficDeviationTrainingConfig{})
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				models := resp.GetModels()
				if len(models) != 1 {
					t.Fatalf("expected 1 model for TrafficDeviation filter, got %d", len(models))
				}
				if models[0].GetMetadata().GetTrainerVersion() != "v1-td" {
					t.Errorf("expected trainer version v1-td, got %s", models[0].GetMetadata().GetTrainerVersion())
				}
			},
		},
		{
			name: "filter_by_scenario_param_matching",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				cfg := &modelpb.TrafficDeviationTrainingConfig{}
				cfg.SetAnomalyThresholdRatio(0.1)
				filter.SetTrafficDeviation(cfg)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 1 {
					t.Fatalf("expected 1 model for matching param ratio 0.1, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "filter_by_scenario_param_mismatch",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				cfg := &modelpb.TrafficDeviationTrainingConfig{}
				cfg.SetAnomalyThresholdRatio(0.99)
				filter.SetTrafficDeviation(cfg)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 0 {
					t.Fatalf("expected 0 models for mismatched ratio 0.99, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "filter_by_metadata_trainer_version",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				metaF := &predictionpb.ModelMetadataFilter{}
				metaF.SetTrainerVersion("v1-td")
				filter.SetMetadataFilter(metaF)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				models := resp.GetModels()
				if len(models) != 1 {
					t.Fatalf("expected 1 model for metadata trainer_version filter, got %d", len(models))
				}
				if models[0].GetMetadata().GetTrainerVersion() != "v1-td" {
					t.Errorf("expected trainer version v1-td, got %s", models[0].GetMetadata().GetTrainerVersion())
				}
			},
		},
		{
			name: "single_model_location_with_matching_filter",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation(pathTD)
				filter := &predictionpb.ModelFilter{}
				filter.SetTrafficDeviation(&modelpb.TrafficDeviationTrainingConfig{})
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 1 {
					t.Fatalf("expected 1 model for valid path + matching filter, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "single_model_location_with_mismatched_filter",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				r.SetModelLocation(pathTD)
				filter := &predictionpb.ModelFilter{}
				filter.SetVolumetricSpike(&modelpb.VolumetricSpikeTrainingConfig{})
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 0 {
					t.Fatalf("expected 0 models for valid TD path + mismatched VS filter, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "filter_by_volumetric_spike_scenario",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				cfg := &modelpb.VolumetricSpikeTrainingConfig{}
				cfg.SetStdDevMultiplier(3.0)
				filter.SetVolumetricSpike(cfg)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 1 {
					t.Fatalf("expected 1 model for matching VolumetricSpike multiplier 3.0, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "filter_by_temporal_deviation_scenario",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				cfg := &modelpb.TemporalDeviationTrainingConfig{}
				cfg.SetSilentThresholdRatio(0.05)
				filter.SetTemporalDeviation(cfg)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 0 {
					t.Fatalf("expected 0 models for unmatched TemporalDeviation filter, got %d", len(resp.GetModels()))
				}
			},
		},
		{
			name: "filter_by_metadata_trainer_version_mismatch",
			req: func() *predictionpb.ListModelsRequest {
				r := &predictionpb.ListModelsRequest{}
				filter := &predictionpb.ModelFilter{}
				metaF := &predictionpb.ModelMetadataFilter{}
				metaF.SetTrainerVersion("v99-nonexistent")
				filter.SetMetadataFilter(metaF)
				r.SetFilter(filter)
				return r
			}(),
			wantCode: codes.OK,
			verify: func(t *testing.T, resp *predictionpb.ListModelsResponse) {
				if len(resp.GetModels()) != 0 {
					t.Fatalf("expected 0 models for non-existent trainer version, got %d", len(resp.GetModels()))
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.before != nil {
				cleanup := test.before(t)
				if cleanup != nil {
					defer cleanup()
				}
			}
			resp, err := s.ListModels(ctx, test.req)
			gotStatus, _ := status.FromError(err)
			if gotStatus.Code() != test.wantCode {
				t.Errorf("ListModels() got code %v, want %v, err: %v", gotStatus.Code(), test.wantCode, err)
			}
			if test.wantCode == codes.OK && test.verify != nil {
				test.verify(t, resp)
			}
		})
	}
}
