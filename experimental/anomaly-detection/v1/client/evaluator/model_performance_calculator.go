// Package evaluator provides utilities to compute model evaluation metrics (e.g. Accuracy, Precision, Recall).
package evaluator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/prototext"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
	findpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/server/proto"
)

// EvaluationMetrics holds the calculated performance metrics of a ML model.
type EvaluationMetrics struct {
	Accuracy  float64
	Precision float64
	Recall    float64
}

type bucketLabel struct {
	isTrueAnomaly    bool
	isPredictAnomaly bool
}

// CalculateModelPerformance streams log entries and findings from disk to evaluate metrics at the entry level without OOM.
//
// Metric Definitions:
//   - Accuracy: (Log Entries where Prediction matches Ground Truth) / (Total Evaluated Log Entries)
//     Formula: (TP + TN) / (TP + TN + FP + FN)
//   - Precision: (Log Entries where Prediction AND Ground Truth are both Anomalous) / (Total Predicted Anomalous Log Entries)
//     Formula: TP / (TP + FP)
//   - Recall: (Log Entries where Prediction AND Ground Truth are both Anomalous) / (Total Ground Truth Anomalous Log Entries)
//     Formula: TP / (TP + FN)
func CalculateModelPerformance(ctx context.Context, testLogPath, findingsPath string) (EvaluationMetrics, error) {
	testFile, err := os.Open(testLogPath)
	if err != nil {
		return EvaluationMetrics{}, err
	}
	defer testFile.Close()
	testReader := bufio.NewReader(testFile)

	findingsFile, err := os.Open(findingsPath)
	if err != nil {
		return EvaluationMetrics{}, err
	}
	defer findingsFile.Close()
	findingsScanner := bufio.NewScanner(findingsFile)

	var tp, fp, tn, fn int

	for {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(testReader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return EvaluationMetrics{}, err
		}

		if !findingsScanner.Scan() {
			if err := findingsScanner.Err(); err != nil {
				return EvaluationMetrics{}, fmt.Errorf("failed to scan findings: %w", err)
			}
			return EvaluationMetrics{}, fmt.Errorf("unexpected end of findings stream: fewer findings than test logs")
		}
		line := findingsScanner.Text()
		finding := &findpb.AnomalyFinding{}
		if err := prototext.Unmarshal([]byte(line), finding); err != nil {
			return EvaluationMetrics{}, err
		}

		prediction := finding.GetIsAnomalousPredict()
		trueLabel := entry.GetIsGroundTruthAnomalous()

		if prediction && trueLabel {
			tp++
		} else if !prediction && !trueLabel {
			tn++
		} else if prediction {
			fp++
		} else {
			fn++
		}
	}

	if findingsScanner.Scan() {
		return EvaluationMetrics{}, fmt.Errorf("unexpected extra findings: more findings than test logs")
	}

	if err := findingsScanner.Err(); err != nil {
		return EvaluationMetrics{}, err
	}

	return computeMetrics(tp, fp, tn, fn), nil
}

// CalculateBucketModelPerformance aggregates individual log entries and their predictions into discrete time buckets, evaluating metrics at the bucket level.
//
// Metric Definitions:
//   - Accuracy: (Time Buckets where Prediction matches Ground Truth) / (Total Evaluated Time Buckets)
//     Formula: (TP + TN) / (TP + TN + FP + FN)
//   - Precision: (Time Buckets where Prediction AND Ground Truth are both Anomalous) / (Total Predicted Anomalous Time Buckets)
//     Formula: TP / (TP + FP)
//   - Recall: (Time Buckets where Prediction AND Ground Truth are both Anomalous) / (Total Ground Truth Anomalous Time Buckets)
//     Formula: TP / (TP + FN)
func CalculateBucketModelPerformance(ctx context.Context, testLogPath, findingsPath string, detectionBucketSize time.Duration) (EvaluationMetrics, error) {
	testFile, err := os.Open(testLogPath)
	if err != nil {
		return EvaluationMetrics{}, err
	}
	defer testFile.Close()
	testReader := bufio.NewReader(testFile)

	findingsFile, err := os.Open(findingsPath)
	if err != nil {
		return EvaluationMetrics{}, err
	}
	defer findingsFile.Close()
	findingsScanner := bufio.NewScanner(findingsFile)

	bucketSizeMs := detectionBucketSize.Milliseconds()
	if bucketSizeMs <= 0 {
		return EvaluationMetrics{}, fmt.Errorf("invalid detection bucket size: %v", detectionBucketSize)
	}

	buckets := make(map[int64]*bucketLabel)

	minBucketIdx := int64(math.MaxInt64)
	maxBucketIdx := int64(math.MinInt64)

	for {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(testReader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return EvaluationMetrics{}, err
		}

		if !findingsScanner.Scan() {
			if err := findingsScanner.Err(); err != nil {
				return EvaluationMetrics{}, fmt.Errorf("failed to scan findings: %w", err)
			}
			return EvaluationMetrics{}, fmt.Errorf("unexpected end of findings stream: fewer findings than test logs")
		}
		line := findingsScanner.Text()
		finding := &findpb.AnomalyFinding{}
		if err := prototext.Unmarshal([]byte(line), finding); err != nil {
			return EvaluationMetrics{}, err
		}

		ts := entry.GetTimestampMs()
		// Group entries by detection bucket slot
		bucketIdx := ts / bucketSizeMs
		minBucketIdx = min(minBucketIdx, bucketIdx)
		maxBucketIdx = max(maxBucketIdx, bucketIdx)

		lbl, ok := buckets[bucketIdx]
		if !ok {
			lbl = &bucketLabel{}
			buckets[bucketIdx] = lbl
		}

		if entry.GetIsGroundTruthAnomalous() {
			lbl.isTrueAnomaly = true
		}
		if finding.GetIsAnomalousPredict() {
			lbl.isPredictAnomaly = true
		}
	}

	if findingsScanner.Scan() {
		return EvaluationMetrics{}, fmt.Errorf("unexpected extra findings: more findings than test logs")
	}

	if err := findingsScanner.Err(); err != nil {
		return EvaluationMetrics{}, err
	}

	// Extend maxBucketIdx to complete the weekend if test logs cut off on Friday night
	for {
		t := time.UnixMilli(maxBucketIdx * bucketSizeMs).UTC()
		if t.Weekday() == time.Monday || (t.Weekday() == time.Sunday && t.Hour() >= 23) {
			break
		}
		maxBucketIdx++
	}

	// Fill in missing time buckets within [minBucketIdx, maxBucketIdx]
	for idx := minBucketIdx; idx <= maxBucketIdx; idx++ {
		if _, ok := buckets[idx]; !ok {
			buckets[idx] = &bucketLabel{
				isTrueAnomaly:    false, // Zero traffic indicates no anomalous spike occurred.
				isPredictAnomaly: false, // Zero traffic generates no logs, so no anomaly is predicted.
			}
		}
	}

	var tp, fp, tn, fn int
	for _, lbl := range buckets {
		if lbl.isTrueAnomaly && lbl.isPredictAnomaly {
			tp++
		} else if !lbl.isTrueAnomaly && !lbl.isPredictAnomaly {
			tn++
		} else if lbl.isPredictAnomaly {
			fp++
		} else {
			fn++
		}
	}

	return computeMetrics(tp, fp, tn, fn), nil
}

// computeMetrics calculates Accuracy, Precision, and Recall from raw counts.
func computeMetrics(tp, fp, tn, fn int) EvaluationMetrics {
	total := tp + fp + tn + fn
	correct := tp + tn

	metrics := EvaluationMetrics{}
	if total > 0 {
		metrics.Accuracy = float64(correct) / float64(total)
	}
	if tp+fp > 0 {
		metrics.Precision = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		metrics.Recall = float64(tp) / float64(tp+fn)
	}
	return metrics
}
