package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protodelim"

	genpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

var (
	// Shared flags for all scenarios.
	configFile = flag.String("config_file", "", "Path to the query template configuration file (.textproto). Required.")
	outputDir  = flag.String("output_dir", "", "Directory path for outputting training/test sets (.binproto). Required.")

	// Scenario 1 (weighted random sampling) flags.
	numLogs = flag.Int("num_logs", 50000, "Total number of logs to generate.")

	// Scenario 2 and 3 (bucket sampling) flags.
	startTimeMs       = flag.Int64("start_time_ms", 0, "Start timestamp of the simulated logs (in milliseconds).")
	trainEndTimeMs    = flag.Int64("train_end_time_ms", 0, "Training/test partition timestamp of the simulated logs (in milliseconds).")
	endTimeMs         = flag.Int64("end_time_ms", 0, "End timestamp of the simulated logs (in milliseconds).")
	bucketSizeMinutes = flag.Int("bucket_size_minutes", 60, "Size of each time bucket in minutes (X minutes).")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	if err := validateSharedFlags(ctx); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	// Load and validate configuration file.
	configs, err := loadConfigs(ctx, *configFile)
	if err != nil {
		log.Fatalf("Failed to load or validate generator config: %v", err)
	}

	if err := validateScenarioFlags(ctx, configs); err != nil {
		log.Fatalf("Invalid flags: %v", err)
	}

	if wSampling := configs.GetWeightedSampling(); wSampling != nil {
		log.Printf("Successfully loaded and validated %d query templates (Weighted Random Sampling).", len(wSampling.GetQueryTemplateConfigs()))
		log.Printf("Configuration applied - Output Dir: %q, Num Logs: %d", *outputDir, *numLogs)
	} else if bSampling := configs.GetBucketSampling(); bSampling != nil {
		log.Printf("Successfully loaded and validated %d query templates (Bucket Sampling).", len(bSampling.GetQueryTemplateConfigs()))
		log.Printf("Configuration applied - Output Dir: %q, Start: %d, End: %d, Bucket Size: %d min", *outputDir, *startTimeMs, *endTimeMs, *bucketSizeMinutes)
	}

	if err := GenerateLogs(ctx, configs, *numLogs, rand.Int63(), *outputDir); err != nil {
		log.Fatalf("Failed to generate logs: %v", err)
	}

	log.Printf("Successfully generated synthetic logs in %q", *outputDir)
}

func validateSharedFlags(ctx context.Context) error {
	if *configFile == "" {
		return fmt.Errorf("required flag --config_file is not set")
	}
	if *outputDir == "" {
		return fmt.Errorf("required flag --output_dir is not set")
	}
	if info, err := os.Stat(*outputDir); err == nil && !info.IsDir() {
		return fmt.Errorf("--output_dir %q exists but is not a directory", *outputDir)
	}
	return nil
}

func validateScenarioFlags(ctx context.Context, configs *genpb.QueryTemplateConfigs) error {
	if configs == nil {
		return fmt.Errorf("configs must be loaded before validating scenario flags")
	}

	// Scenario 2: Bucket sampling.
	if configs.GetBucketSampling() != nil {
		if *bucketSizeMinutes <= 0 {
			return fmt.Errorf("--bucket_size_minutes must be positive, got %d", *bucketSizeMinutes)
		}
		if *startTimeMs <= 0 {
			return fmt.Errorf("--start_time_ms must be positive, got %d", *startTimeMs)
		}
		if *endTimeMs <= *startTimeMs {
			return fmt.Errorf("--end_time_ms must be greater than start_time_ms")
		}
		if *trainEndTimeMs <= *startTimeMs || *trainEndTimeMs > *endTimeMs {
			return fmt.Errorf("--train_end_time_ms must be between start_time_ms and end_time_ms, got %d", *trainEndTimeMs)
		}
	} else {
		// Scenario 1: Weighted sampling.
		if *numLogs <= 0 {
			return fmt.Errorf("--num_logs must be a positive integer, got %d", *numLogs)
		}
	}
	return nil
}

// GenerateLogs samples and writes synthetic database audit logs to a single logs.binproto file.
func GenerateLogs(ctx context.Context, configs *genpb.QueryTemplateConfigs, numLogs int, seed int64, outputDir string) error {
	if !filepath.IsAbs(outputDir) {
		if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
			outputDir = filepath.Join(workspaceDir, outputDir)
		}
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	file, err := os.Create(filepath.Join(outputDir, "logs.binproto"))
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	defer w.Flush()

	rng := rand.New(rand.NewSource(seed))

	// Scenario 2: Bucket sampling.
	if configs.GetBucketSampling() != nil {
		return GenerateLogsBucketByBucket(configs, rng, w)
	}

	// Scenario 1: Weighted random sampling.
	return GenerateLogsWeighted(configs, numLogs, rng, w)
}

// GenerateLogsWeighted generates weighted random sampling audit logs.
func GenerateLogsWeighted(configs *genpb.QueryTemplateConfigs, numLogs int, rng *rand.Rand, w io.Writer) error {
	sampler := NewSampler(configs.GetWeightedSampling().GetQueryTemplateConfigs(), rng.Float64)
	generator := NewLogGenerator(rng)

	for i := 0; i < numLogs; i++ {
		cfg := sampler.Sample()
		if err := generator.GenerateLog(cfg, w); err != nil {
			return fmt.Errorf("failed to generate random log: %w", err)
		}
	}
	return nil
}

// GenerateLogsBucketByBucket generates bucket-based audit logs bucket-by-bucket and writes them to the writer.
func GenerateLogsBucketByBucket(configs *genpb.QueryTemplateConfigs, rng *rand.Rand, w io.Writer) error {
	startTime := time.UnixMilli(*startTimeMs)
	endTime := time.UnixMilli(*endTimeMs)
	trainEndTime := time.UnixMilli(*trainEndTimeMs)
	bucketSize := time.Duration(*bucketSizeMinutes) * time.Minute
	bucketSizeMs := bucketSize.Milliseconds()

	generator := NewLogGenerator(rng)
	// Iterate through each bucket in the time window.
	for current := startTime; current.Before(endTime); current = current.Add(bucketSize) {
		isTrainingPhase := current.Before(trainEndTime)

		// Generate and buffer logs for all templates in the current bucket.
		var logs []*genpb.AuditLogEntry
		for _, cfg := range configs.GetBucketSampling().GetQueryTemplateConfigs() {
			// Anomalous spikes only occur in the testing phase.
			isSpike := false
			if !isTrainingPhase && cfg.GetBucketConfig() != nil {
				isSpike = rng.Float64() < cfg.GetBucketConfig().GetAnomalousSpikeProbability()
			}

			count := SampleQueryCount(cfg, isSpike, current.Hour(), current.Weekday(), rng)

			for i := 0; i < count; i++ {
				timestampMs := SampleTimestampMs(cfg, current, bucketSizeMs, rng)
				entry := generator.BuildBucketLog(cfg, isSpike, timestampMs)
				logs = append(logs, entry)
			}
		}

		// Sort logs chronologically to mirror real-world database log sequence.
		sort.Slice(logs, func(i, j int) bool {
			return logs[i].GetTimestampMs() < logs[j].GetTimestampMs()
		})

		for _, entry := range logs {
			if _, err := protodelim.MarshalTo(w, entry); err != nil {
				return fmt.Errorf("failed to generate bucket log: %w", err)
			}
		}
	}
	return nil
}
