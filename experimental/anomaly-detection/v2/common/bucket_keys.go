package common

import (
	"fmt"
	"time"
)

// AbsoluteBucketKey identifies a bucket by db_user, query template, and absolute bucket index.
type AbsoluteBucketKey struct {
	DbUser              string
	QueryTemplate       string
	AbsoluteBucketIndex int64
}

// ToCycleBucketKey maps the AbsoluteBucketKey to a recurring CycleBucketKey based on buckets per cycle.
func (k AbsoluteBucketKey) ToCycleBucketKey(bucketsPerCycle int64) CycleBucketKey {
	if bucketsPerCycle <= 0 {
		panic("common: bucketsPerCycle must be positive")
	}
	return CycleBucketKey{
		DbUser:        k.DbUser,
		QueryTemplate: k.QueryTemplate,
		BucketIndex:   k.AbsoluteBucketIndex % bucketsPerCycle,
	}
}

// CycleBucketKey identifies a recurring cycle bucket by db_user, query template, and bucket index.
type CycleBucketKey struct {
	DbUser        string
	QueryTemplate string
	BucketIndex   int64
}

// GetAbsoluteBucketKey maps extracted log features and bucket duration to an AbsoluteBucketKey.
func GetAbsoluteBucketKey(feature ExtractedLogFeatures, bucketDuration time.Duration) AbsoluteBucketKey {
	if bucketDuration < time.Millisecond {
		panic("common: bucketDuration must be at least 1 millisecond")
	}
	bucketDurationMs := bucketDuration.Milliseconds()
	return AbsoluteBucketKey{
		DbUser:              feature.DbUser,
		QueryTemplate:       feature.QueryTemplate,
		AbsoluteBucketIndex: feature.TimestampMs / bucketDurationMs,
	}
}

// ValidateDurations validates bucketDuration and cycleDuration, and returns bucketsPerCycle.
func ValidateDurations(bucketDuration time.Duration, cycleDuration time.Duration) (int64, error) {
	if bucketDuration < time.Millisecond {
		return 0, fmt.Errorf("%w: bucketDuration must be at least 1 millisecond", ErrInvalidArgument)
	}
	if cycleDuration <= 0 {
		return 0, fmt.Errorf("%w: cycleDuration must be positive", ErrInvalidArgument)
	}
	if bucketDuration > cycleDuration {
		return 0, fmt.Errorf("%w: bucketDuration must be less than or equal to cycleDuration", ErrInvalidArgument)
	}
	if cycleDuration%bucketDuration != 0 {
		return 0, fmt.Errorf("%w: bucketDuration must evenly divide the cycleDuration", ErrInvalidArgument)
	}
	return int64(cycleDuration / bucketDuration), nil
}
