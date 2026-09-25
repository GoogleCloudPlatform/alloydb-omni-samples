// Package common provides shared utilities for AlloyDB anomaly detection.
package common

import (
	"errors"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v2/common/proto"
)

// ErrInvalidArgument represents an invalid argument error.
var ErrInvalidArgument = errors.New("invalid argument")

// ErrNotFound represents a resource not found error.
var ErrNotFound = errors.New("not found")

// ExtractedLogFeatures represents the features (e.g., query template) extracted from a log entry.
type ExtractedLogFeatures struct {
	DbUser        string
	QueryTemplate string
	TimestampMs   int64
}

// Extract selects and converts the raw protobuf log entry into strong-typed ExtractedLogFeatures.
func Extract(logEntry *auditpb.AuditLogEntry) (ExtractedLogFeatures, error) {
	if logEntry == nil {
		return ExtractedLogFeatures{}, ErrInvalidArgument
	}
	return ExtractedLogFeatures{
		DbUser:        logEntry.GetDbUser(),
		QueryTemplate: logEntry.GetNormalizedQueryTemplate(),
		TimestampMs:   logEntry.GetTimestampMs(),
	}, nil
}
