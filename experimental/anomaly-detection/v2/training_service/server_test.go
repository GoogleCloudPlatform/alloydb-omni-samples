package main

import (
	"context"
	"errors"
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
	trainingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/training_service/proto"
)

// mockPusher implements ModelPusher interface for testing RPC push interactions.
type mockPusher struct {
	pushFunc func(ctx context.Context, modelLocation string, model *modelpb.Model) error
}

func (m *mockPusher) Push(ctx context.Context, modelLocation string, model *modelpb.Model) error {
	if m.pushFunc != nil {
		return m.pushFunc(ctx, modelLocation, model)
	}
	return nil
}

// createTestLogFile creates a binary log file with a single AuditLogEntry.
func createTestLogFile(ctx context.Context, t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create test log file: %v", err)
	}
	defer file.Close()

	entry := &auditpb.AuditLogEntry{}
	entry.SetNormalizedQueryTemplate("SELECT * FROM users WHERE age > %v")
	entry.SetQueryStatement("SELECT * FROM users WHERE age > 20")
	entry.SetDbUser("db_user")
	entry.SetDbName("db_name")
	entry.SetIsGroundTruthAnomalous(false)
	entry.SetTimestampMs(time.Now().UnixMilli())

	if _, err := protodelim.MarshalTo(file, entry); err != nil {
		t.Fatalf("failed to write test log entry: %v", err)
	}
}

func defaultCycleConfig() *modelpb.CycleConfig {
	c := &modelpb.CycleConfig{}
	c.SetBucketDuration(durationpb.New(time.Hour))
	c.SetCycleDuration(durationpb.New(7 * 24 * time.Hour))
	return c
}

func newTrafficConfig(ratio float64) *trainingpb.ModelTrainingConfig {
	c := &trainingpb.ModelTrainingConfig{}
	cfg := &modelpb.TrafficDeviationTrainingConfig{}
	cfg.SetAnomalyThresholdRatio(ratio)
	c.SetTrafficDeviationConfig(cfg)
	return c
}

func newVolumetricConfig(multiplier float64, cycle *modelpb.CycleConfig) *trainingpb.ModelTrainingConfig {
	c := &trainingpb.ModelTrainingConfig{}
	cfg := &modelpb.VolumetricSpikeTrainingConfig{}
	cfg.SetStdDevMultiplier(multiplier)
	if cycle != nil {
		cfg.SetCycleConfig(cycle)
	}
	c.SetVolumetricSpikeConfig(cfg)
	return c
}

func newTemporalConfig(ratio float64, cycle *modelpb.CycleConfig) *trainingpb.ModelTrainingConfig {
	c := &trainingpb.ModelTrainingConfig{}
	cfg := &modelpb.TemporalDeviationTrainingConfig{}
	cfg.SetSilentThresholdRatio(ratio)
	if cycle != nil {
		cfg.SetCycleConfig(cycle)
	}
	c.SetTemporalDeviationConfig(cfg)
	return c
}

func newReq(path string, configs ...*trainingpb.ModelTrainingConfig) *trainingpb.TrainModelsRequest {
	r := &trainingpb.TrainModelsRequest{}
	r.SetTrainLogPath(path)
	r.SetTrainingConfigs(configs)
	return r
}

func TestTrainModels(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	validPath := filepath.Join(tmpDir, "valid.binproto")
	createTestLogFile(ctx, t, validPath)

	corruptPath := filepath.Join(tmpDir, "corrupt.binproto")
	if err := os.WriteFile(corruptPath, []byte{0x0A, 0xFF}, 0644); err != nil {
		t.Fatalf("failed to create corrupt file: %v", err)
	}

	nonExistentPath := filepath.Join(tmpDir, "non_existent.binproto")

	tests := []struct {
		name         string
		req          *trainingpb.TrainModelsRequest
		pusher       ModelPusher
		overrideDir  string
		wantCode     codes.Code
		verifyModels bool
		wantCount    int
	}{
		// Phase 1: Request Validation & Parameter Constraints
		{
			name:     "invalid_request_empty_train_log_path",
			req:      newReq("", newTrafficConfig(0.2)),
			wantCode: codes.InvalidArgument,
		},
		{
			name: "invalid_request_empty_training_configs",
			req: func() *trainingpb.TrainModelsRequest {
				r := &trainingpb.TrainModelsRequest{}
				r.SetTrainLogPath(validPath)
				return r
			}(),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "scenario1_traffic_deviation_invalid_ratio_excessive",
			req:      newReq(validPath, newTrafficConfig(1.5)),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "scenario2_volumetric_spike_invalid_multiplier_negative",
			req:      newReq(validPath, newVolumetricConfig(-1.0, defaultCycleConfig())),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "scenario3_temporal_deviation_invalid_ratio_negative",
			req:      newReq(validPath, newTemporalConfig(-0.01, defaultCycleConfig())),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "init_trainer_unsupported_config_case",
			req:      newReq(validPath, &trainingpb.ModelTrainingConfig{}),
			wantCode: codes.Internal,
		},

		// Phase 2: Log File Ingestion & File Stream Parsing
		{
			name:     "log_ingestion_file_not_found",
			req:      newReq(nonExistentPath, newTrafficConfig(0.2)),
			wantCode: codes.NotFound,
		},
		{
			name:     "log_ingestion_corrupt_binary_data",
			req:      newReq(corruptPath, newTrafficConfig(0.2)),
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "log_ingestion_internal_error",
			req:      newReq(tmpDir, newTrafficConfig(0.2)),
			wantCode: codes.Internal,
		},

		// Phase 3: Model Persistence & RPC Push Errors
		{
			// Verifies error handling when modelDir is non-existent or unwritable.
			name:        "model_persistence_disk_write_error",
			req:         newReq(validPath, newTrafficConfig(0.2)),
			overrideDir: "/proc/invalid_non_existent_dir",
			wantCode:    codes.Internal,
		},
		{
			// Verifies error handling when PredictionService push RPC fails.
			name: "rpc_push_service_unavailable",
			req:  newReq(validPath, newTrafficConfig(0.2)),
			pusher: &mockPusher{
				pushFunc: func(ctx context.Context, loc string, m *modelpb.Model) error {
					return errors.New("prediction service unavailable")
				},
			},
			wantCode: codes.Internal,
		},

		// Phase 4: Happy Paths - End-to-End Single & Multi-Model Training
		{
			name:         "happy_path_single_model_traffic_deviation",
			req:          newReq(validPath, newTrafficConfig(0.2)),
			wantCode:     codes.OK,
			verifyModels: true,
			wantCount:    1,
		},
		{
			name:         "happy_path_single_model_volumetric_spike",
			req:          newReq(validPath, newVolumetricConfig(3.0, defaultCycleConfig())),
			wantCode:     codes.OK,
			verifyModels: true,
			wantCount:    1,
		},
		{
			name:         "happy_path_single_model_temporal_deviation",
			req:          newReq(validPath, newTemporalConfig(0.05, defaultCycleConfig())),
			wantCode:     codes.OK,
			verifyModels: true,
			wantCount:    1,
		},
		{
			name:         "happy_path_multi_model_single_pass",
			req:          newReq(validPath, newTrafficConfig(0.2), newVolumetricConfig(3.0, defaultCycleConfig()), newTemporalConfig(0.05, defaultCycleConfig())),
			wantCode:     codes.OK,
			verifyModels: true,
			wantCount:    3,
		},
		{
			// Verifies end-to-end model training with successful RPC push to PredictionService.
			name: "happy_path_pusher_enabled_successful_rpc_push",
			req:  newReq(validPath, newTrafficConfig(0.2)),
			pusher: &mockPusher{
				pushFunc: func(ctx context.Context, loc string, m *modelpb.Model) error {
					if loc == "" || m == nil {
						t.Errorf("pusher received invalid location or nil model")
					}
					return nil
				},
			},
			wantCode:     codes.OK,
			verifyModels: true,
			wantCount:    1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Set target modelDir (defaulting to tmpDir unless explicitly overridden)
			if test.overrideDir != "" {
				*modelDir = test.overrideDir
			} else {
				*modelDir = tmpDir
			}

			s := NewTrainingServer(test.pusher)
			resp, err := s.TrainModels(ctx, test.req)

			gotStatus, _ := status.FromError(err)
			if gotStatus.Code() != test.wantCode {
				t.Fatalf("TrainModels() got code %v, want code %v, error: %v", gotStatus.Code(), test.wantCode, err)
			}

			if test.wantCode == codes.OK {
				if resp == nil {
					t.Fatalf("TrainModels() returned nil response unexpectedly")
				}
				results := resp.GetResults()
				if len(results) != test.wantCount {
					t.Errorf("got %d trained model results, want %d", len(results), test.wantCount)
				}

				if test.verifyModels {
					for _, res := range results {
						modelLoc := res.GetModelLocation()
						if modelLoc == "" {
							t.Errorf("expected model location to be populated")
						}

						data, err := os.ReadFile(modelLoc)
						if err != nil {
							t.Fatalf("failed to read created model file %q: %v", modelLoc, err)
						}
						t.Cleanup(func() {
							os.Remove(modelLoc)
						})

						model := &modelpb.Model{}
						if err := proto.Unmarshal(data, model); err != nil {
							t.Fatalf("failed to unmarshal saved model: %v", err)
						}

						if !model.HasMetadata() {
							t.Errorf("expected model to have metadata")
						} else {
							metadata := model.GetMetadata()
							if metadata.GetTrainingTime() == nil {
								t.Errorf("expected TrainingTime to be set")
							}
							if metadata.GetTotalLogEntriesProcessed() != 1 {
								t.Errorf("TotalLogEntriesProcessed got %d, want 1", metadata.GetTotalLogEntriesProcessed())
							}
						}
					}
				}
			}
		})
	}
}

func TestNewTrainingServerAndPusher(t *testing.T) {
	t.Run("empty_pusher_close_noop", func(t *testing.T) {
		p := &GRPCPusher{}
		if err := p.Close(); err != nil {
			t.Errorf("p.Close() got error %v, want nil", err)
		}
	})

	t.Run("server_initialization", func(t *testing.T) {
		s := NewTrainingServer(nil)
		if s == nil {
			t.Fatalf("NewTrainingServer(nil) returned nil")
		}
	})
}
