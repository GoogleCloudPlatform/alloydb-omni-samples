// Package main implements a gRPC service for AlloyDB anomaly detection model training.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/training_service/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/local"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	modelpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
	predictiongrpc "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/proto"
	predictionpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/prediction_service/proto"
	traininggrpc "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/training_service/proto"
	trainingpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/training_service/proto"
)

var (
	modelDir              = flag.String("model_dir", "/tmp", "Directory to save trained models")
	port                  = flag.String("port", "50051", "The server port")
	predictionServiceAddr = flag.String("prediction_service_addr", "", "The prediction service address to push models to")
)

// ModelPusher defines the interface for pushing models to a prediction service.
type ModelPusher interface {
	Push(ctx context.Context, modelLocation string, model *modelpb.Model) error
}

// GRPCPusher implements ModelPusher using gRPC.
type GRPCPusher struct {
	conn   *grpc.ClientConn
	client predictiongrpc.PredictionServiceClient
}

// NewGRPCPusher creates a new GRPCPusher with a persistent connection.
func NewGRPCPusher(addr string) (*GRPCPusher, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(local.NewCredentials()))
	if err != nil {
		return nil, err
	}
	client := predictiongrpc.NewPredictionServiceClient(conn)
	return &GRPCPusher{conn: conn, client: client}, nil
}

// Push pushes the model to the prediction service.
func (p *GRPCPusher) Push(ctx context.Context, modelLocation string, model *modelpb.Model) error {
	req := &predictionpb.PushModelRequest{}
	req.SetModelLocation(modelLocation)
	req.SetModel(model)

	_, err := p.client.PushModel(ctx, req)
	return err
}

// Close closes the underlying gRPC connection.
func (p *GRPCPusher) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

type trainingServer struct {
	traininggrpc.UnimplementedTrainingServiceServer
	pusher ModelPusher
}

// NewTrainingServer creates a new TrainingServiceServer.
func NewTrainingServer(pusher ModelPusher) traininggrpc.TrainingServiceServer {
	return &trainingServer{pusher: pusher}
}

// initTrainer creates a trainer based on the provided training config.
func initTrainer(config *trainingpb.ModelTrainingConfig) (service.Trainer, error) {
	switch config.WhichConfig() {
	case trainingpb.ModelTrainingConfig_TrafficDeviationConfig_case:
		cfg := config.GetTrafficDeviationConfig()
		return service.NewTrafficDeviationTrainer(cfg.GetAnomalyThresholdRatio())
	case trainingpb.ModelTrainingConfig_VolumetricSpikeConfig_case:
		cfg := config.GetVolumetricSpikeConfig()
		bucketDuration := cfg.GetCycleConfig().GetBucketDuration().AsDuration()
		cycleDuration := cfg.GetCycleConfig().GetCycleDuration().AsDuration()
		return service.NewVolumetricSpikeTrainer(
			cfg.GetStdDevMultiplier(),
			bucketDuration,
			cycleDuration,
		)
	case trainingpb.ModelTrainingConfig_TemporalDeviationConfig_case:
		cfg := config.GetTemporalDeviationConfig()
		bucketDuration := cfg.GetCycleConfig().GetBucketDuration().AsDuration()
		cycleDuration := cfg.GetCycleConfig().GetCycleDuration().AsDuration()
		return service.NewTemporalDeviationTrainer(
			cfg.GetSilentThresholdRatio(),
			bucketDuration,
			cycleDuration,
		)
	default:
		return nil, fmt.Errorf("unknown or unsupported training config")
	}
}

// TrainModels trains one or more anomaly models in a single pass, saves them to storage, and pushes them to the prediction service.
func (s *trainingServer) TrainModels(ctx context.Context, req *trainingpb.TrainModelsRequest) (*trainingpb.TrainModelsResponse, error) {
	// Validate request
	if req == nil || req.GetTrainLogPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid request: train_log_path is required")
	}

	if len(req.GetTrainingConfigs()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no training_configs provided")
	}

	// Initialize trainers for each training config
	var trainers []service.Trainer
	for _, cfg := range req.GetTrainingConfigs() {
		t, err := initTrainer(cfg)
		if err != nil {
			if errors.Is(err, service.ErrInvalidArgument) {
				return nil, status.Errorf(codes.InvalidArgument, "failed to initialize trainer: %v", err)
			}
			return nil, status.Errorf(codes.Internal, "failed to initialize trainer: %v", err)
		}
		trainers = append(trainers, t)
	}

	// Ingest logs and train all models in a single pass
	trainLogPath := common.ResolvePath(req.GetTrainLogPath())
	err := common.IngestBinProtoStream(ctx, trainLogPath, func(entry *auditpb.AuditLogEntry) error {
		features, err := common.Extract(entry)
		if err != nil {
			return err
		}
		for _, t := range trainers {
			t.Count(features)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, common.ErrInvalidArgument) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid input data: %v", err)
		}
		if errors.Is(err, common.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "training log file not found: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to ingest logs: %v", err)
	}

	// Build, save, and push each model
	var results []*trainingpb.TrainedModelResult
	for i, t := range trainers {
		res, err := s.saveAndPush(ctx, t.BuildModel(), i)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}

	resp := &trainingpb.TrainModelsResponse{}
	resp.SetResults(results)
	resp.SetTrainingSummary(fmt.Sprintf("Successfully trained %d model(s) in a single pass.", len(results)))
	return resp, nil
}

func (s *trainingServer) saveAndPush(ctx context.Context, model *modelpb.Model, idx int) (*trainingpb.TrainedModelResult, error) {
	modelLocation := filepath.Join(*modelDir, fmt.Sprintf("model_v2_%d_%d.bin", time.Now().UnixNano(), idx))
	modelData, err := proto.Marshal(model)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to marshal model: %v", err)
	}
	if err := os.WriteFile(modelLocation, modelData, 0644); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save model to %q: %v", modelLocation, err)
	}

	if s.pusher != nil {
		if err := s.pusher.Push(ctx, modelLocation, model); err != nil {
			log.Printf("Failed to push model to prediction service: %v", err)
			return nil, status.Errorf(codes.Internal, "failed to push model to prediction service: %v", err)
		}
	}

	res := &trainingpb.TrainedModelResult{}
	res.SetModelLocation(modelLocation)
	res.SetMetadata(model.GetMetadata())
	return res, nil
}

func main() {
	flag.Parse()

	lis, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		log.Fatalf("Failed to listen on port %s: %v", *port, err)
	}

	var pusher *GRPCPusher
	if *predictionServiceAddr != "" {
		var err error
		pusher, err = NewGRPCPusher(*predictionServiceAddr)
		if err != nil {
			log.Fatalf("Failed to connect to prediction service: %v", err)
		}
		defer pusher.Close()
	}

	grpcServer := grpc.NewServer()
	srv := NewTrainingServer(pusher)
	traininggrpc.RegisterTrainingServiceServer(grpcServer, srv)

	log.Printf("Starting gRPC TrainingServiceServer listening on :%s", *port)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
