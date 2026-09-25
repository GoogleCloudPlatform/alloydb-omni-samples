// Package main implements an evaluation client orchestrating experiments for AlloyDB anomaly detection.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/client/evaluator"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/client/visualizer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/local"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/prototext"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
	cfgpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
	pb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/server/proto"
	pbgrpc "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/server/proto"
)

type stringListValue struct {
	target *[]string
}

func (s *stringListValue) String() string {
	if s.target == nil {
		return ""
	}
	return strings.Join(*s.target, ",")
}

func (s *stringListValue) Set(val string) error {
	if val == "" {
		*s.target = nil
		return nil
	}
	*s.target = strings.Split(val, ",")
	return nil
}

func stringListFlag(name string, value []string, usage string) *[]string {
	res := append([]string(nil), value...)
	flag.Var(&stringListValue{target: &res}, name, usage)
	return &res
}

var (
	serverAddr = flag.String("server_addr", "localhost:50051", "The address of the anomaly detection gRPC server")

	// Scenario 1 Generator flags
	configPath   = flag.String("config_path", "v1/client/query_template_config.textproto", "Path to the query template configuration")
	generatorBin = flag.String("generator_bin", "./v1/generator/src/generator", "Path to the synthetic log generator binary")
	outputDir    = flag.String("output_dir", "", "Directory path for training and testing logs. If empty, a self-cleaning temp directory will be created.")
	numLogs      = flag.Int("num_logs", 500000, "Experiment logs count")

	// Scenario 1 Service Application flags
	anomalyThresholdRatio = flag.Float64("anomaly_threshold_ratio", 0.10, "Anomaly threshold ratio for training")

	// Scenario 1 Traffic Deviation Experiment flags
	testRatio                  = flag.Float64("test_ratio", 0.10, "Test dataset ratio")
	anomalyThresholdScalesFlag = stringListFlag("anomaly_threshold_scales", []string{"0.01", "0.02", "0.03", "0.035", "0.04", "0.08", "0.16", "0.32"}, "Comma-separated list of anomaly threshold scales for the experiment")
	scalingLogCountsFlag       = stringListFlag("scaling_log_counts", []string{"100", "200", "500", "1000", "2000", "5000", "10000", "25000", "50000"}, "Comma-separated list of log counts for scaling experiment")
	testRatioScalesFlag        = stringListFlag("test_ratio_scales", []string{"0.10", "0.30", "0.50", "0.70", "0.90", "0.95", "0.99", "0.999"}, "Comma-separated list of test ratios for the experiment")

	// Scenario 2 (Spike Traffic) and Scenario 3 (Temporal Deviation) Generator flags
	startTimeMs       = flag.Int64("start_time_ms", 1716199200000, "Start timestamp in milliseconds for the log generator of Scenario 2 (Spike Traffic) Experiment")
	trainEndTimeMs    = flag.Int64("train_end_time_ms", 1722247200000, "Train partition end timestamp for the log generator")
	endTimeMs         = flag.Int64("end_time_ms", 1723456800000, "End timestamp for the log generator")
	bucketSizeMinutes = flag.Int("bucket_size_minutes", 60, "Time bucket size in minutes for the baseline log generator")

	// Scenario 2 (Spike Traffic) and Scenario 3 (Temporal Deviation) Service Application flags
	periodMinutes              = flag.Int("period_minutes", 10080, "Period duration in minutes for periodic anomaly detection models")
	detectionBucketSizeMinutes = flag.Int("detection_bucket_size_minutes", 60, "Detection bucket size in minutes for periodic baseline models")

	// Scenario 2 (Spike Traffic) and Scenario 3 (Temporal Deviation) Experiment flags
	detectionBucketSizeMinutesScalesFlag = stringListFlag("bucket_size_minutes_scales", []string{"5", "10", "15", "30", "45", "60", "90", "120", "180", "240"}, "Detection bucket size scales list for Scenario 2 (Spike Traffic) and Scenario 3 (Temporal Deviation) Experiments")

	// Scenario 2 (Spike Traffic) Service Application flag
	stdDevMultiplier = flag.Float64("std_dev_multiplier", 4.0, "Standard deviation multiplier for Scenario 2 (Spike Traffic) Experiment baseline")

	// Scenario 2 (Spike Traffic) Experiment flags
	trainDurationWeeksScalesFlag   = stringListFlag("train_duration_weeks_scales", []string{"1", "2", "3", "4", "6", "8", "10", "12", "14", "16", "18", "20"}, "Training duration scales in weeks for Scenario 2 (Spike Traffic) and Scenario 3 (Temporal Deviation) Experiments")
	stdDevMultiplierScalesFlag     = stringListFlag("std_dev_multiplier_scales", []string{"0.1", "0.25", "0.5", "0.75", "1.0", "1.5", "2.0", "2.5", "3.0", "3.5", "4.0", "5.0", "6.0", "7.0", "8.0"}, "Multipliers list for Scenario 2 (Spike Traffic) Experiment")
	anomalousSpikeMultiplierScales = stringListFlag("anomalous_spike_multiplier_scales", []string{"1.2", "1.5", "1.75", "2.0", "2.5", "3.0", "4.0", "6.0", "10.0"}, "Anomaly spike multipliers list for Scenario 2 (Spike Traffic) Experiment")

	// Scenario 3 (Temporal Deviation) Service Application flags
	silentThresholdRatio = flag.Float64("silent_threshold_ratio", 0.001, "Periodic time slots with a query count less than this ratio of total training queries are classified as silent.")

	// Scenario 3 (Temporal Deviation) Experiment flags
	trainDurationDaysScalesFlag    = stringListFlag("train_duration_days_scales", []string{"1", "2", "3", "5", "7", "14", "21", "28", "35", "42"}, "Training duration scales in days for Scenario 3 (Temporal Deviation) Experiment")
	silentThresholdRatioScales     = stringListFlag("silent_threshold_ratio_scales", []string{"0.0001", "0.0005", "0.0006", "0.0007", "0.0008", "0.0009", "0.001", "0.002", "0.005", "0.006", "0.007", "0.008", "0.009", "0.01", "0.02", "0.05", "0.10"}, "Comma-separated list of silent threshold ratio scales for Scenario 3 (Temporal Deviation)")
	weekendTrafficMultiplierScales = stringListFlag("weekend_traffic_multiplier_scales", []string{"0.0", "0.01", "0.02", "0.03", "0.05", "0.08", "0.10", "0.11", "0.12", "0.13", "0.15", "0.20", "0.30", "0.50"}, "Weekend traffic multiplier scales list for Scenario 3 (Temporal Deviation) Weekend Traffic Multiplier Experiment")
)

type experimentResult struct {
	param   string
	metrics evaluator.EvaluationMetrics
}

// validateFlags checks that the command-line flags are valid.
func validateFlags() error {
	if *configPath == "" {
		return fmt.Errorf("--config_path cannot be empty")
	}

	// Scenario 1 (Traffic Deviation)
	if *numLogs <= 0 {
		return fmt.Errorf("--num_logs must be a positive integer, got %d", *numLogs)
	}

	inRange := func(v float64) bool { return v > 0.0 && v < 1.0 }
	isPositiveFloat := func(v float64) bool { return v > 0.0 }
	isPositiveInt := func(v int) bool { return v > 0 }

	if !inRange(*anomalyThresholdRatio) {
		return fmt.Errorf("--anomaly_threshold_ratio must be between 0.0 and 1.0 (exclusive), got %f", *anomalyThresholdRatio)
	}

	if !inRange(*testRatio) {
		return fmt.Errorf("--test_ratio must be between 0.0 and 1.0 (exclusive), got %f", *testRatio)
	}

	if err := validateFloatList(*anomalyThresholdScalesFlag, inRange, "anomaly threshold scale must be between 0.0 and 1.0 (exclusive), got %f"); err != nil {
		return fmt.Errorf("invalid --anomaly_threshold_scales: %w", err)
	}

	if err := validateIntList(*scalingLogCountsFlag, isPositiveInt, "scaling log count must be a positive integer, got %d"); err != nil {
		return fmt.Errorf("invalid --scaling_log_counts: %w", err)
	}

	if err := validateFloatList(*testRatioScalesFlag, inRange, "test ratio scale must be between 0.0 and 1.0 (exclusive), got %f"); err != nil {
		return fmt.Errorf("invalid --test_ratio_scales: %w", err)
	}

	// Scenario 2 (Spike Traffic)
	if *startTimeMs <= 0 {
		return fmt.Errorf("--start_time_ms must be positive, got %d", *startTimeMs)
	}
	if *trainEndTimeMs <= *startTimeMs {
		return fmt.Errorf("--train_end_time_ms (%d) must be strictly after --start_time_ms (%d)", *trainEndTimeMs, *startTimeMs)
	}
	if *endTimeMs <= *trainEndTimeMs {
		return fmt.Errorf("--end_time_ms (%d) must be strictly after --train_end_time_ms (%d)", *endTimeMs, *trainEndTimeMs)
	}
	if *bucketSizeMinutes <= 0 {
		return fmt.Errorf("--bucket_size_minutes must be positive, got %d", *bucketSizeMinutes)
	}

	bucketSizeMs := int64(*bucketSizeMinutes) * 60 * 1000
	if *startTimeMs%bucketSizeMs != 0 {
		return fmt.Errorf("--start_time_ms must be aligned to the bucket size (%d min / %d ms), got %d (remainder: %d)", *bucketSizeMinutes, bucketSizeMs, *startTimeMs, *startTimeMs%bucketSizeMs)
	}
	if *trainEndTimeMs%bucketSizeMs != 0 {
		return fmt.Errorf("--train_end_time_ms must be aligned to the bucket size (%d min / %d ms), got %d (remainder: %d)", *bucketSizeMinutes, bucketSizeMs, *trainEndTimeMs, *trainEndTimeMs%bucketSizeMs)
	}
	if *endTimeMs%bucketSizeMs != 0 {
		return fmt.Errorf("--end_time_ms must be aligned to the bucket size (%d min / %d ms), got %d (remainder: %d)", *bucketSizeMinutes, bucketSizeMs, *endTimeMs, *endTimeMs%bucketSizeMs)
	}

	if *stdDevMultiplier <= 0.0 {
		return fmt.Errorf("--std_dev_multiplier must be positive, got %f", *stdDevMultiplier)
	}
	if *detectionBucketSizeMinutes <= 0 {
		return fmt.Errorf("--detection_bucket_size_minutes must be positive, got %d", *detectionBucketSizeMinutes)
	}
	if *periodMinutes <= 0 {
		return fmt.Errorf("--period_minutes must be positive, got %d", *periodMinutes)
	}
	if *periodMinutes%*detectionBucketSizeMinutes != 0 {
		return fmt.Errorf("--detection_bucket_size_minutes (%d) must evenly divide --period_minutes (%d)", *detectionBucketSizeMinutes, *periodMinutes)
	}

	if err := validateFloatList(*stdDevMultiplierScalesFlag, isPositiveFloat, "std_dev_multiplier scale must be positive, got %f"); err != nil {
		return fmt.Errorf("invalid --std_dev_multiplier_scales: %w", err)
	}
	dividesPeriod := func(v int) bool {
		return v > 0 && *periodMinutes%v == 0
	}
	if err := validateIntList(*detectionBucketSizeMinutesScalesFlag, dividesPeriod, "bucket_size_minutes scale must be positive and evenly divide --period_minutes, got %d"); err != nil {
		return fmt.Errorf("invalid --bucket_size_minutes_scales: %w", err)
	}
	if err := validateFloatList(*anomalousSpikeMultiplierScales, isPositiveFloat, "anomalous_spike_multiplier scale must be positive, got %f"); err != nil {
		return fmt.Errorf("invalid --anomalous_spike_multiplier_scales: %w", err)
	}
	if err := validateIntList(*trainDurationWeeksScalesFlag, isPositiveInt, "train_duration_weeks scale must be positive, got %d"); err != nil {
		return fmt.Errorf("invalid --train_duration_weeks_scales: %w", err)
	}

	// Scenario 3 (Temporal Deviation)
	if !inRange(*silentThresholdRatio) {
		return fmt.Errorf("--silent_threshold_ratio must be between 0.0 and 1.0 (exclusive), got %f", *silentThresholdRatio)
	}
	if err := validateFloatList(*silentThresholdRatioScales, inRange, "silent_threshold_ratio scale must be between 0.0 and 1.0 (exclusive), got %f"); err != nil {
		return fmt.Errorf("invalid --silent_threshold_ratio_scales: %w", err)
	}
	if err := validateIntList(*trainDurationDaysScalesFlag, isPositiveInt, "train_duration_days scale must be positive, got %d"); err != nil {
		return fmt.Errorf("invalid --train_duration_days_scales: %w", err)
	}

	isNonNegativeFloat := func(v float64) bool { return v >= 0.0 }
	if err := validateFloatList(*weekendTrafficMultiplierScales, isNonNegativeFloat, "weekend_traffic_multiplier scale must be non-negative, got %f"); err != nil {
		return fmt.Errorf("invalid --weekend_traffic_multiplier_scales: %w", err)
	}

	return nil
}

func mustParseFloatList(list []string) []float64 {
	res := make([]float64, 0, len(list))
	for _, s := range list {
		val, err := strconv.ParseFloat(s, 64)
		if err != nil {
			panic(fmt.Sprintf("failed to parse float element %q: %v", s, err))
		}
		res = append(res, val)
	}
	return res
}

func mustParseIntList(list []string) []int {
	res := make([]int, 0, len(list))
	for _, s := range list {
		val, err := strconv.Atoi(s)
		if err != nil {
			panic(fmt.Sprintf("failed to parse int element %q: %v", s, err))
		}
		res = append(res, val)
	}
	return res
}

// runLogGenerator runs the synthetic log generator binary with the provided arguments.
func runLogGenerator(ctx context.Context, configFilePath string, outDir string, extraArgs ...string) error {
	configAbs, err := filepath.Abs(configFilePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for config: %w", err)
	}

	args := append([]string{
		fmt.Sprintf("--config_file=%s", configAbs),
		fmt.Sprintf("--output_dir=%s", outDir),
	}, extraArgs...)

	cmd := exec.CommandContext(ctx, *generatorBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("generator execution failed: %w", err)
	}
	return nil
}

func splitLogs(ctx context.Context, logsPath, trainLogAbs, testLogAbs string, filter func(entry *auditpb.AuditLogEntry, idx int) (writeToTrain, writeToTest bool)) error {
	file, err := os.Open(logsPath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)

	trainFile, err := os.Create(trainLogAbs)
	if err != nil {
		return err
	}
	defer trainFile.Close()
	trainWriter := bufio.NewWriter(trainFile)

	testFile, err := os.Create(testLogAbs)
	if err != nil {
		return err
	}
	defer testFile.Close()
	testWriter := bufio.NewWriter(testFile)

	for i := 0; ; i++ {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(reader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		writeToTrain, writeToTest := filter(entry, i)
		if writeToTrain {
			if _, err := protodelim.MarshalTo(trainWriter, entry); err != nil {
				return err
			}
		}
		if writeToTest {
			if _, err := protodelim.MarshalTo(testWriter, entry); err != nil {
				return err
			}
		}
	}
	if err := trainWriter.Flush(); err != nil {
		return err
	}
	return testWriter.Flush()
}

func splitLogsByRatioStreaming(ctx context.Context, logsPath, trainLogAbs, testLogAbs string, ratio float64, totalCount int) error {
	trainLimit := totalCount - int(float64(totalCount)*ratio)

	return splitLogs(ctx, logsPath, trainLogAbs, testLogAbs, func(entry *auditpb.AuditLogEntry, i int) (writeToTrain, writeToTest bool) {
		return i < trainLimit, i >= trainLimit && i < totalCount
	})
}

func runTrainAndPredict(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, trainReq *pb.TrainModelRequest, testLogAbs string) (string, error) {
	if _, err := client.TrainModel(ctx, trainReq); err != nil {
		return "", fmt.Errorf("TrainModel failed: %w", err)
	}

	predictReq := &pb.PredictRequest{}
	predictReq.SetPredictLogPath(testLogAbs)
	predictResp, err := client.Predict(ctx, predictReq)
	if err != nil {
		return "", fmt.Errorf("Predict failed: %w", err)
	}

	findingsPath := predictResp.GetFindingsPath()
	if findingsPath == "" {
		return "", fmt.Errorf("received empty findings path from server")
	}

	return findingsPath, nil
}

func runDeviationTrafficEvaluationPipeline(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, outDir string, threshold float64) (evaluator.EvaluationMetrics, error) {
	trainLogAbs := filepath.Join(outDir, "train.binproto")
	testLogAbs := filepath.Join(outDir, "test.binproto")

	trainReq := &pb.TrainModelRequest{}
	trainReq.SetTrainLogPath(trainLogAbs)
	cfg := &pb.TemplateProbabilityConfig{}
	cfg.SetAnomalyThresholdRatio(threshold)
	trainReq.SetTemplateProbabilityConfig(cfg)

	findingsPath, err := runTrainAndPredict(ctx, client, trainReq, testLogAbs)
	if err != nil {
		return evaluator.EvaluationMetrics{}, err
	}

	metrics, err := evaluator.CalculateModelPerformance(ctx, testLogAbs, findingsPath)
	if err != nil {
		return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to calculate model performance: %w", err)
	}
	return metrics, nil
}

func printResultsTable(w io.Writer, title, colName string, results []experimentResult) {
	fmt.Fprintf(w, "\n=== %s ===\n", title)
	fmt.Fprintf(w, "| %-18s | %-10s | %-10s | %-10s |\n", colName, "Accuracy", "Precision", "Recall")
	fmt.Fprintf(w, "|--------------------|------------|------------|------------|\n")
	for _, res := range results {
		fmt.Fprintf(w, "| %-18s | %-10.2f%% | %-10.2f%% | %-10.2f%% |\n",
			res.param,
			res.metrics.Accuracy*100.0,
			res.metrics.Precision*100.0,
			res.metrics.Recall*100.0,
		)
	}
	fmt.Fprintln(w)
}

func runVaryingAnomalyThresholdRatioExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, expDir string) ([]experimentResult, error) {
	log.Println("Starting Varying Anomaly Threshold Ratio Experiment...")
	anomalyThresholdScales := mustParseFloatList(*anomalyThresholdScalesFlag)
	var results []experimentResult

	for _, thresholdVal := range anomalyThresholdScales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running with anomaly threshold: %.2f%%", thresholdVal*100.0)

		metrics, err := runDeviationTrafficEvaluationPipeline(ctx, client, expDir, thresholdVal)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for anomaly threshold %.2f: %w", thresholdVal, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.1f%%", thresholdVal*100.0),
			metrics: metrics,
		})
	}
	return results, nil
}

func runScalingNumOfLogsExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir string) ([]experimentResult, error) {
	log.Println("Starting Scaling Num of Logs Experiment...")
	scalingLogCounts := mustParseIntList(*scalingLogCountsFlag)
	var results []experimentResult

	for _, logCountValue := range scalingLogCounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		log.Printf("Running with %d logs", logCountValue)
		expDir := filepath.Join(tempDir, fmt.Sprintf("scaling_%d", logCountValue))

		if err := runLogGenerator(ctx, *configPath, expDir, fmt.Sprintf("--num_logs=%d", logCountValue)); err != nil {
			return nil, fmt.Errorf("failed to run generator for log count %d: %w", logCountValue, err)
		}

		logsPath := filepath.Join(expDir, "logs.binproto")
		trainLogAbs := filepath.Join(expDir, "train.binproto")
		testLogAbs := filepath.Join(expDir, "test.binproto")
		if err := splitLogsByRatioStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *testRatio, logCountValue); err != nil {
			return nil, fmt.Errorf("failed to split logs: %w", err)
		}

		metrics, err := runDeviationTrafficEvaluationPipeline(ctx, client, expDir, *anomalyThresholdRatio)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for log count %d: %w", logCountValue, err)
		}
		os.RemoveAll(expDir)

		results = append(results, experimentResult{
			param:   strconv.Itoa(logCountValue),
			metrics: metrics,
		})
	}
	return results, nil
}

func runVaryingTestRatioExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir, baseExpDir string) ([]experimentResult, error) {
	log.Println("Starting Varying Test Ratio Experiment...")
	testRatioScales := mustParseFloatList(*testRatioScalesFlag)
	var results []experimentResult

	runDir := filepath.Join(tempDir, "test_ratio_run_0")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create run directory: %w", err)
	}
	defer os.RemoveAll(runDir)

	for _, ratioVal := range testRatioScales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running with test ratio: %.1f%%", ratioVal*100.0)

		logsPath := filepath.Join(baseExpDir, "logs.binproto")
		trainLogAbs := filepath.Join(runDir, "train.binproto")
		testLogAbs := filepath.Join(runDir, "test.binproto")
		if err := splitLogsByRatioStreaming(ctx, logsPath, trainLogAbs, testLogAbs, ratioVal, *numLogs); err != nil {
			return nil, fmt.Errorf("failed to split logs: %w", err)
		}

		metrics, err := runDeviationTrafficEvaluationPipeline(ctx, client, runDir, *anomalyThresholdRatio)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for test ratio %.3f: %w", ratioVal, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.1f%%", ratioVal*100.0),
			metrics: metrics,
		})
	}
	return results, nil
}

func main() {
	flag.Parse()
	ctx := context.Background()

	if err := run(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(ctx context.Context) error {
	if err := validateFlags(); err != nil {
		return fmt.Errorf("invalid flags: %w", err)
	}

	log.Printf("Connecting to gRPC server at: %s", *serverAddr)
	conn, err := grpc.NewClient(*serverAddr, grpc.WithTransportCredentials(local.NewCredentials()))
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer conn.Close()

	client := pbgrpc.NewAnomalyDetectionServiceClient(conn)

	var tempDir string
	var cleanup bool

	if *outputDir != "" {
		tempDir = *outputDir
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory %q: %w", tempDir, err)
		}
	} else {
		tempDir, err = os.MkdirTemp("", "anomaly_experiments_*")
		if err != nil {
			return fmt.Errorf("failed to create temp dir: %w", err)
		}
		cleanup = true
	}

	if cleanup {
		defer os.RemoveAll(tempDir)
	}

	configs, err := readConfig(ctx, *configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Scenario 2 Spike Traffic Experiments.
	if configs.GetBucketSampling() != nil {
		log.Println("Detected Scenario-2 (Bucket Sampling) Configuration. Running Scenario-2 Experiments...")

		// Generate base logs once for shared use in std dev multiplier and detection bucket size experiments
		baseExpDir := filepath.Join(tempDir, "scenario2_base")
		if err := runLogGenerator(ctx, *configPath, baseExpDir,
			fmt.Sprintf("--start_time_ms=%d", *startTimeMs),
			fmt.Sprintf("--train_end_time_ms=%d", *trainEndTimeMs),
			fmt.Sprintf("--end_time_ms=%d", *endTimeMs),
			fmt.Sprintf("--bucket_size_minutes=%d", *bucketSizeMinutes),
		); err != nil {
			return fmt.Errorf("failed to generate base logs: %w", err)
		}

		// Generate weekly request traffic waveform plot once
		if err := visualizer.GenerateTrafficWaveform(ctx, baseExpDir, *bucketSizeMinutes, *startTimeMs, *trainEndTimeMs, *endTimeMs); err != nil {
			log.Printf("failed to generate traffic waveform plot: %v", err)
		}

		// Pre-split the base logs once
		baseLogsPath := filepath.Join(baseExpDir, "logs.binproto")
		baseTrainLogAbs := filepath.Join(baseExpDir, "train.binproto")
		baseTestLogAbs := filepath.Join(baseExpDir, "test.binproto")
		if err := splitLogsByTimeStreaming(ctx, baseLogsPath, baseTrainLogAbs, baseTestLogAbs, *trainEndTimeMs, 0); err != nil {
			return fmt.Errorf("failed to split base logs: %w", err)
		}

		// Scenario 3 (Temporal Deviation) Experiments.
		isScenario3 := false
		for _, cfg := range configs.GetBucketSampling().GetQueryTemplateConfigs() {
			if cfg.GetBucketConfig().HasWeekendTrafficMultiplier() {
				isScenario3 = true
				break
			}
		}

		if isScenario3 {
			log.Println("Detected Scenario-3 (Silent Bucket) Configuration. Running Scenario-3 Pipeline...")

			_, findingsPath, err := runTemporalDeviationEvaluationPipeline(ctx, client, baseTrainLogAbs, baseTestLogAbs, *silentThresholdRatio, *detectionBucketSizeMinutes)
			if err != nil {
				return fmt.Errorf("failed to run prediction for default ratio: %w", err)
			}

			if err := visualizer.GeneratePredictionWaveform(ctx, baseLogsPath, findingsPath, baseExpDir, *bucketSizeMinutes, *startTimeMs, *trainEndTimeMs, *endTimeMs, *silentThresholdRatio); err != nil {
				log.Printf("failed to generate prediction plot: %v", err)
			}

			trainDurationResults, err := runTemporalDeviationTrainingDurationExperiment(ctx, client, tempDir, baseExpDir)
			if err != nil {
				return fmt.Errorf("scaling training duration experiment failed: %w", err)
			}

			thresholdRatioResults, err := runVaryingSilentThresholdRatioExperiment(ctx, client, baseTrainLogAbs, baseTestLogAbs)
			if err != nil {
				return fmt.Errorf("varying silent threshold ratio experiment failed: %w", err)
			}

			optimalPointResults, err := runOptimalSilentThresholdPointExperiment(ctx, client, tempDir, configs)
			if err != nil {
				return fmt.Errorf("finding optimal silent threshold point experiment failed: %w", err)
			}

			bucketSizeResults, err := runVaryingSilentBucketSizeExperiment(ctx, client, baseTrainLogAbs, baseTestLogAbs)
			if err != nil {
				return fmt.Errorf("varying silent bucket size experiment failed: %w", err)
			}

			randomAttackWithNoiseResults, err := runRandomAttackWithVaryingWeekendTrafficExperiment(ctx, client, tempDir, configs)
			if err != nil {
				return fmt.Errorf("random attack with varying weekend traffic experiment failed: %w", err)
			}

			printResultsTable(os.Stdout, "Scaling Training Duration Experiment", "Training Duration", trainDurationResults)
			printResultsTable(os.Stdout, "Varying Silent Threshold Ratio Experiment", "Threshold Ratio", thresholdRatioResults)
			printResultsTable(os.Stdout, "Scaling Detection Bucket Size Minutes Experiment", "Bucket Size", bucketSizeResults)
			for ratioLabel, resList := range optimalPointResults {
				printResultsTable(os.Stdout, fmt.Sprintf("Finding Optimal Silent Threshold Ratio Point Experiment (%s)", ratioLabel), "Weekend Multiplier", resList)
			}
			printResultsTable(os.Stdout, "Random Attack with Varying Weekend Traffic Experiment (4.0x Spike)", "Weekend Multiplier", randomAttackWithNoiseResults)

			return nil
		}

		trainDurationResults, err := runScalingTrainingDurationExperiment(ctx, client, tempDir)
		if err != nil {
			return fmt.Errorf("scaling training duration experiment failed: %w", err)
		}

		stdDevMultiplierResults, err := runVaryingStdDevMultiplierExperiment(ctx, client, baseExpDir)
		if err != nil {
			return fmt.Errorf("varying std dev multiplier experiment failed: %w", err)
		}

		bucketSizeResults, err := runScalingDetectionBucketSizeMinutesExperiment(ctx, client, baseExpDir)
		if err != nil {
			return fmt.Errorf("scaling detection bucket size minutes experiment failed: %w", err)
		}

		spikeMultiplierResults, err := runScalingAnomalousSpikeMultiplierExperiment(ctx, client, tempDir)
		if err != nil {
			return fmt.Errorf("scaling anomalous spike multiplier experiment failed: %w", err)
		}

		printResultsTable(os.Stdout, "Scaling Training Duration Experiment", "Training Duration", trainDurationResults)
		printResultsTable(os.Stdout, "Varying Standard Deviation Multiplier Experiment", "StdDev Multiplier", stdDevMultiplierResults)
		printResultsTable(os.Stdout, "Scaling Detection Bucket Size Minutes Experiment", "Bucket Size", bucketSizeResults)
		printResultsTable(os.Stdout, "Scaling Anomalous Spike Multiplier Experiment", "Spike Multiplier", spikeMultiplierResults)
		return nil
	}

	log.Println("Detected Scenario-1 (Weighted Sampling) Configuration. Running Scenario-1 Experiments...")

	// Scenario 1 Traffic Deviation Experiment.
	// Generate base logs once for shared use in anomaly threshold and test ratio experiments
	baseExpDir := filepath.Join(tempDir, "scenario1_base")
	if err := runLogGenerator(ctx, *configPath, baseExpDir, fmt.Sprintf("--num_logs=%d", *numLogs)); err != nil {
		return fmt.Errorf("failed to generate base logs: %w", err)
	}

	// Pre-split base logs once for anomaly threshold experiment (which uses default testRatio)
	baseLogsPath := filepath.Join(baseExpDir, "logs.binproto")
	baseTrainLogAbs := filepath.Join(baseExpDir, "train.binproto")
	baseTestLogAbs := filepath.Join(baseExpDir, "test.binproto")
	if err := splitLogsByRatioStreaming(ctx, baseLogsPath, baseTrainLogAbs, baseTestLogAbs, *testRatio, *numLogs); err != nil {
		return fmt.Errorf("failed to split base logs: %w", err)
	}

	// Scenario-1 Traffic Deviation Experiment Experiments.
	anomalyThresholdResults, err := runVaryingAnomalyThresholdRatioExperiment(ctx, client, baseExpDir)
	if err != nil {
		return fmt.Errorf("varying anomaly threshold ratio experiment failed: %w", err)
	}

	scalingLogResults, err := runScalingNumOfLogsExperiment(ctx, client, tempDir)
	if err != nil {
		return fmt.Errorf("scaling num of logs experiment failed: %w", err)
	}

	testRatioResults, err := runVaryingTestRatioExperiment(ctx, client, tempDir, baseExpDir)
	if err != nil {
		return fmt.Errorf("varying test ratio experiment failed: %w", err)
	}

	printResultsTable(os.Stdout, "Varying Anomaly Threshold Ratio Experiment", "Anomaly Threshold", anomalyThresholdResults)
	printResultsTable(os.Stdout, "Scaling Num of Logs Experiment", "Num of Logs", scalingLogResults)
	printResultsTable(os.Stdout, "Varying Test Ratio Experiment", "Test Ratio", testRatioResults)
	return nil
}

func readConfig(ctx context.Context, path string) (*cfgpb.QueryTemplateConfigs, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	configs := &cfgpb.QueryTemplateConfigs{}
	if err := prototext.Unmarshal(data, configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// splitLogsByTimeStreaming splits logs by a time boundary and a training duration limit.
// Logs before partitionTimeMs are training logs (filtered by durationDays), logs after are test logs.
func splitLogsByTimeStreaming(ctx context.Context, logsPath, trainLogAbs, testLogAbs string, partitionTimeMs int64, durationDays int) error {
	const dayMs int64 = 24 * 3600 * 1000
	startTimeMs := partitionTimeMs - int64(durationDays)*dayMs

	return splitLogs(ctx, logsPath, trainLogAbs, testLogAbs, func(entry *auditpb.AuditLogEntry, i int) (writeToTrain, writeToTest bool) {
		ts := entry.GetTimestampMs()
		if ts < partitionTimeMs {
			// Negative or zero durationDays bypasses the training start boundary filter.
			return durationDays <= 0 || ts >= startTimeMs, false
		}
		return false, true
	})
}

func runSpikeTrafficEvaluationPipeline(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, outDir string, stdDevMultiplier float64, bucketSizeMin int) (evaluator.EvaluationMetrics, error) {
	trainLogAbs := filepath.Join(outDir, "train.binproto")
	testLogAbs := filepath.Join(outDir, "test.binproto")

	trainReq := &pb.TrainModelRequest{}
	trainReq.SetTrainLogPath(trainLogAbs)
	s2Cfg := &pb.PeriodicRequestBaselineConfig{}
	s2Cfg.SetStdDevMultiplier(stdDevMultiplier)
	s2Cfg.SetBucketSizeMinutes(int32(bucketSizeMin))
	s2Cfg.SetPeriodMinutes(int32(*periodMinutes))
	trainReq.SetPeriodicRequestBaselineConfig(s2Cfg)

	findingsPath, err := runTrainAndPredict(ctx, client, trainReq, testLogAbs)
	if err != nil {
		return evaluator.EvaluationMetrics{}, err
	}

	bucketSize := time.Duration(bucketSizeMin) * time.Minute
	metrics, err := evaluator.CalculateBucketModelPerformance(ctx, testLogAbs, findingsPath, bucketSize)
	if err != nil {
		return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to calculate model performance: %w", err)
	}
	return metrics, nil
}

func runTemporalDeviationEvaluationPipeline(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, trainLogAbs, testLogAbs string, silentRatio float64, bucketSizeMin int) (evaluator.EvaluationMetrics, string, error) {
	trainReq := &pb.TrainModelRequest{}
	trainReq.SetTrainLogPath(trainLogAbs)
	s3Cfg := &pb.SilentBucketConfig{}
	s3Cfg.SetSilentThresholdRatio(silentRatio)
	s3Cfg.SetBucketSizeMinutes(int32(bucketSizeMin))
	s3Cfg.SetPeriodMinutes(int32(*periodMinutes))
	trainReq.SetSilentBucketConfig(s3Cfg)

	findingsPath, err := runTrainAndPredict(ctx, client, trainReq, testLogAbs)
	if err != nil {
		return evaluator.EvaluationMetrics{}, "", err
	}

	bucketSize := time.Duration(bucketSizeMin) * time.Minute
	metrics, err := evaluator.CalculateBucketModelPerformance(ctx, testLogAbs, findingsPath, bucketSize)
	if err != nil {
		return evaluator.EvaluationMetrics{}, "", fmt.Errorf("failed to calculate model performance: %w", err)
	}
	return metrics, findingsPath, nil
}

func validateFloatList(list []string, validateVal func(float64) bool, errMsg string) error {
	for _, valStr := range list {
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return fmt.Errorf("invalid float element %q: %w", valStr, err)
		}
		if !validateVal(val) {
			return fmt.Errorf(errMsg, val)
		}
	}
	return nil
}

func validateIntList(list []string, validateVal func(int) bool, errMsg string) error {
	for _, valStr := range list {
		val, err := strconv.Atoi(valStr)
		if err != nil {
			return fmt.Errorf("invalid int element %q: %w", valStr, err)
		}
		if !validateVal(val) {
			return fmt.Errorf(errMsg, val)
		}
	}
	return nil
}

func runTemporalDeviationTrainingDurationExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir, baseExpDir string) ([]experimentResult, error) {
	log.Println("Starting Temporal Deviation Training Duration Experiment...")
	trainDurationScales := mustParseIntList(*trainDurationDaysScalesFlag)
	var results []experimentResult

	for _, days := range trainDurationScales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for training duration: %d days", days)

		metrics, err := func() (evaluator.EvaluationMetrics, error) {
			runDir := filepath.Join(tempDir, fmt.Sprintf("train_duration_run_%d", days))
			if err := os.MkdirAll(runDir, 0755); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to create run directory: %w", err)
			}
			defer os.RemoveAll(runDir)

			logsPath := filepath.Join(baseExpDir, "logs.binproto")
			trainLogAbs := filepath.Join(runDir, "train.binproto")
			testLogAbs := filepath.Join(runDir, "test.binproto")
			if err := splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *trainEndTimeMs, days); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to split logs: %w", err)
			}

			m, _, err := runTemporalDeviationEvaluationPipeline(ctx, client, trainLogAbs, testLogAbs, *silentThresholdRatio, *detectionBucketSizeMinutes)
			return m, err
		}()
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for training duration %d: %w", days, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%d days", days),
			metrics: metrics,
		})
	}
	return results, nil
}

func runVaryingSilentThresholdRatioExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, baseTrainLogAbs, baseTestLogAbs string) ([]experimentResult, error) {
	log.Println("Starting Varying Silent Threshold Ratio Experiment...")
	scales := mustParseFloatList(*silentThresholdRatioScales)
	var results []experimentResult

	for _, val := range scales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for silent_threshold_ratio: %.4f%%", val*100.0)

		m, _, err := runTemporalDeviationEvaluationPipeline(ctx, client, baseTrainLogAbs, baseTestLogAbs, val, *detectionBucketSizeMinutes)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for silent_threshold_ratio %.4f: %w", val, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.4f%%", val*100.0),
			metrics: m,
		})
	}
	return results, nil
}

func runVaryingSilentBucketSizeExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, baseTrainLogAbs, baseTestLogAbs string) ([]experimentResult, error) {
	log.Println("Starting Varying Silent Bucket Size Experiment...")
	scales := mustParseIntList(*detectionBucketSizeMinutesScalesFlag)
	var results []experimentResult

	for _, bSize := range scales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for bucket_size: %d min", bSize)

		m, _, err := runTemporalDeviationEvaluationPipeline(ctx, client, baseTrainLogAbs, baseTestLogAbs, *silentThresholdRatio, bSize)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for bucket size %d: %w", bSize, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%d min", bSize),
			metrics: m,
		})
	}
	return results, nil
}

func runScalingTrainingDurationExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir string) ([]experimentResult, error) {
	log.Println("Starting Scaling Training Duration Experiment...")
	trainDurationScales := mustParseIntList(*trainDurationWeeksScalesFlag)
	var results []experimentResult

	// Find the maximum training duration in weeks to determine the start time for log generation.
	maxWeeks := 4
	for _, w := range trainDurationScales {
		maxWeeks = max(maxWeeks, w)
	}
	const weekMs int64 = 7 * 24 * 3600 * 1000
	startTimeMsForGen := *trainEndTimeMs - int64(maxWeeks)*weekMs

	// Generate shared logs for training duration experiment sweep once.
	log.Println("Generating shared logs for training duration sweep once...")
	trainDurationBaseDir := filepath.Join(tempDir, "train_duration_base")
	if err := os.MkdirAll(trainDurationBaseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}
	defer os.RemoveAll(trainDurationBaseDir)

	if err := runLogGenerator(ctx, *configPath, trainDurationBaseDir,
		fmt.Sprintf("--start_time_ms=%d", startTimeMsForGen),
		fmt.Sprintf("--train_end_time_ms=%d", *trainEndTimeMs),
		fmt.Sprintf("--end_time_ms=%d", *endTimeMs),
		fmt.Sprintf("--bucket_size_minutes=%d", *bucketSizeMinutes),
	); err != nil {
		return nil, fmt.Errorf("failed to run generator for training duration: %w", err)
	}

	// Run evaluation for each training duration.
	for _, weeks := range trainDurationScales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for training duration: %d weeks", weeks)

		metrics, err := func() (evaluator.EvaluationMetrics, error) {
			runDir := filepath.Join(tempDir, fmt.Sprintf("train_duration_run_%d", weeks))
			if err := os.MkdirAll(runDir, 0755); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to create run directory: %w", err)
			}
			defer os.RemoveAll(runDir)

			logsPath := filepath.Join(trainDurationBaseDir, "logs.binproto")
			trainLogAbs := filepath.Join(runDir, "train.binproto")
			testLogAbs := filepath.Join(runDir, "test.binproto")
			if err := splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *trainEndTimeMs, weeks*7); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to split logs: %w", err)
			}

			return runSpikeTrafficEvaluationPipeline(ctx, client, runDir, *stdDevMultiplier, *detectionBucketSizeMinutes)
		}()
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for training duration %d: %w", weeks, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%d weeks", weeks),
			metrics: metrics,
		})
	}

	return results, nil
}

func runVaryingStdDevMultiplierExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, expDir string) ([]experimentResult, error) {
	log.Println("Starting Varying Standard Deviation Multiplier Experiment...")
	stdDevMultiplierScalesList := mustParseFloatList(*stdDevMultiplierScalesFlag)
	var results []experimentResult

	for _, multiplierVal := range stdDevMultiplierScalesList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for std_dev_multiplier: %.2f", multiplierVal)

		metrics, err := runSpikeTrafficEvaluationPipeline(ctx, client, expDir, multiplierVal, *detectionBucketSizeMinutes)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for std_dev_multiplier %.2f: %w", multiplierVal, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.2f", multiplierVal),
			metrics: metrics,
		})
	}
	return results, nil
}

func runScalingDetectionBucketSizeMinutesExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, expDir string) ([]experimentResult, error) {
	log.Println("Starting Scaling Detection Bucket Size Minutes Experiment...")
	bucketSizeScalesList := mustParseIntList(*detectionBucketSizeMinutesScalesFlag)
	var results []experimentResult

	for _, bSize := range bucketSizeScalesList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for bucket_size: %d minutes", bSize)

		metrics, err := runSpikeTrafficEvaluationPipeline(ctx, client, expDir, *stdDevMultiplier, bSize)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for bucket size %d: %w", bSize, err)
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%d min", bSize),
			metrics: metrics,
		})
	}
	return results, nil
}

func runScalingAnomalousSpikeMultiplierExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir string) ([]experimentResult, error) {
	log.Println("Starting Scaling Anomalous Spike Multiplier Experiment...")
	spikeMultiplierScalesList := mustParseFloatList(*anomalousSpikeMultiplierScales)

	configs, err := readConfig(ctx, *configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read base config: %w", err)
	}

	var results []experimentResult
	for _, multiplier := range spikeMultiplierScalesList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for anomalous_spike_multiplier: %.2f", multiplier)

		// Mutate anomalous spike multiplier in-place.
		if configs.GetBucketSampling() != nil {
			for _, cfg := range configs.GetBucketSampling().GetQueryTemplateConfigs() {
				if cfg.GetBucketConfig() != nil {
					cfg.GetBucketConfig().SetAnomalousSpikeMultiplier(multiplier)
				}
			}
		}

		// Write mutated configs to a reusable temporary config file.
		tempConfigPath := filepath.Join(tempDir, "config_spike.textproto")
		bytes, err := prototext.Marshal(configs)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal mutated configs: %w", err)
		}
		if err := os.WriteFile(tempConfigPath, bytes, 0644); err != nil {
			return nil, fmt.Errorf("failed to write temp config file: %w", err)
		}

		expDir := filepath.Join(tempDir, fmt.Sprintf("spike_multiplier_%.2f", multiplier))
		if err := runLogGenerator(ctx, tempConfigPath, expDir,
			fmt.Sprintf("--start_time_ms=%d", *startTimeMs),
			fmt.Sprintf("--train_end_time_ms=%d", *trainEndTimeMs),
			fmt.Sprintf("--end_time_ms=%d", *endTimeMs),
			fmt.Sprintf("--bucket_size_minutes=%d", *bucketSizeMinutes),
		); err != nil {
			return nil, fmt.Errorf("failed to run generator for spike multiplier %.2f: %w", multiplier, err)
		}

		logsPath := filepath.Join(expDir, "logs.binproto")
		trainLogAbs := filepath.Join(expDir, "train.binproto")
		testLogAbs := filepath.Join(expDir, "test.binproto")
		if err := splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *trainEndTimeMs, 0); err != nil {
			return nil, fmt.Errorf("failed to split logs: %w", err)
		}

		metrics, err := runSpikeTrafficEvaluationPipeline(ctx, client, expDir, *stdDevMultiplier, *detectionBucketSizeMinutes)
		if err != nil {
			return nil, fmt.Errorf("evaluation failed for spike multiplier %.2f: %w", multiplier, err)
		}
		os.RemoveAll(expDir)

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.2f", multiplier),
			metrics: metrics,
		})
	}
	return results, nil
}

func runOptimalSilentThresholdPointExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir string, baseConfig *cfgpb.QueryTemplateConfigs) (map[string][]experimentResult, error) {
	log.Println("Starting Finding Optimal Silent Threshold Point Experiment...")
	candidateRatios := []float64{0.0001, 0.0002, 0.0003, 0.0004}
	weekendScales := mustParseFloatList(*weekendTrafficMultiplierScales)
	allResults := make(map[string][]experimentResult)

	for _, ratio := range candidateRatios {
		ratioLabel := fmt.Sprintf("%.4f%% (%.4f)", ratio*100.0, ratio)
		log.Printf("Evaluating candidate ratio: %s", ratioLabel)

		var resList []experimentResult
		for _, wMult := range weekendScales {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			configs, err := readConfig(ctx, *configPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read configs: %w", err)
			}
			if configs.GetBucketSampling() != nil {
				for _, cfg := range configs.GetBucketSampling().GetQueryTemplateConfigs() {
					if cfg.GetBucketConfig() != nil {
						cfg.GetBucketConfig().SetWeekendTrafficMultiplier(wMult)
					}
				}
			}

			tempConfigPath := filepath.Join(tempDir, "config_optimal.textproto")
			bytes, err := prototext.Marshal(configs)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal mutated configs: %w", err)
			}
			if err := os.WriteFile(tempConfigPath, bytes, 0644); err != nil {
				return nil, fmt.Errorf("failed to write temp config file: %w", err)
			}

			expDir := filepath.Join(tempDir, fmt.Sprintf("optimal_ratio_%.4f_w_%.3f", ratio, wMult))
			if err := runLogGenerator(ctx, tempConfigPath, expDir,
				fmt.Sprintf("--start_time_ms=%d", *startTimeMs),
				fmt.Sprintf("--train_end_time_ms=%d", *trainEndTimeMs),
				fmt.Sprintf("--end_time_ms=%d", *endTimeMs),
				fmt.Sprintf("--bucket_size_minutes=%d", *bucketSizeMinutes),
			); err != nil {
				os.RemoveAll(expDir)
				return nil, fmt.Errorf("failed to run generator for ratio %.4f w %.3f: %w", ratio, wMult, err)
			}

			logsPath := filepath.Join(expDir, "logs.binproto")
			trainLogAbs := filepath.Join(expDir, "train.binproto")
			testLogAbs := filepath.Join(expDir, "test.binproto")
			if err := splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *trainEndTimeMs, 0); err != nil {
				os.RemoveAll(expDir)
				return nil, fmt.Errorf("failed to split logs: %w", err)
			}

			m, _, err := runTemporalDeviationEvaluationPipeline(ctx, client, trainLogAbs, testLogAbs, ratio, *detectionBucketSizeMinutes)
			os.RemoveAll(expDir)
			if err != nil {
				return nil, fmt.Errorf("evaluation failed for ratio %.4f w %.3f: %w", ratio, wMult, err)
			}

			resList = append(resList, experimentResult{
				param:   fmt.Sprintf("%.3fw", wMult),
				metrics: m,
			})
		}
		allResults[ratioLabel] = resList
	}

	return allResults, nil
}

func runRandomAttackWithVaryingWeekendTrafficExperiment(ctx context.Context, client pbgrpc.AnomalyDetectionServiceClient, tempDir string, baseConfig *cfgpb.QueryTemplateConfigs) ([]experimentResult, error) {
	log.Println("Starting Random Attack with Varying Weekend Traffic Experiment...")
	scales := mustParseFloatList(*weekendTrafficMultiplierScales)
	var results []experimentResult

	for _, val := range scales {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Printf("Running evaluation for weekend_traffic_multiplier: %.3fw", val)

		metrics, err := func() (evaluator.EvaluationMetrics, error) {
			configs, err := readConfig(ctx, *configPath)
			if err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to read configs: %w", err)
			}
			if configs.GetBucketSampling() != nil {
				for _, cfg := range configs.GetBucketSampling().GetQueryTemplateConfigs() {
					if cfg.GetBucketConfig() != nil {
						cfg.GetBucketConfig().SetAnomalousSpikeProbability(0.05)
						cfg.GetBucketConfig().SetAnomalousSpikeMultiplier(4.0)
						cfg.GetBucketConfig().SetWeekendTrafficMultiplier(val)
					}
				}
			}

			tempConfigPath := filepath.Join(tempDir, "config_random_weekend.textproto")
			bytes, err := prototext.Marshal(configs)
			if err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to marshal mutated configs: %w", err)
			}
			if err := os.WriteFile(tempConfigPath, bytes, 0644); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to write temp config file: %w", err)
			}

			expDir := filepath.Join(tempDir, fmt.Sprintf("random_weekend_multiplier_%.3f", val))
			defer os.RemoveAll(expDir)
			if err := runLogGenerator(ctx, tempConfigPath, expDir,
				fmt.Sprintf("--start_time_ms=%d", *startTimeMs),
				fmt.Sprintf("--train_end_time_ms=%d", *trainEndTimeMs),
				fmt.Sprintf("--end_time_ms=%d", *endTimeMs),
				fmt.Sprintf("--bucket_size_minutes=%d", *bucketSizeMinutes),
			); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to run generator for random weekend multiplier %.3f: %w", val, err)
			}

			logsPath := filepath.Join(expDir, "logs.binproto")
			trainLogAbs := filepath.Join(expDir, "train.binproto")
			testLogAbs := filepath.Join(expDir, "test.binproto")
			if err := splitLogsByTimeStreaming(ctx, logsPath, trainLogAbs, testLogAbs, *trainEndTimeMs, 0); err != nil {
				return evaluator.EvaluationMetrics{}, fmt.Errorf("failed to split logs: %w", err)
			}

			m, _, err := runTemporalDeviationEvaluationPipeline(ctx, client, trainLogAbs, testLogAbs, *silentThresholdRatio, *detectionBucketSizeMinutes)
			return m, err
		}()
		if err != nil {
			return nil, err
		}

		results = append(results, experimentResult{
			param:   fmt.Sprintf("%.3fw", val),
			metrics: metrics,
		})
	}
	return results, nil
}
