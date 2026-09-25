package common

import (
	"testing"
	"time"
)

func TestToCycleBucketKey(t *testing.T) {
	tests := []struct {
		name            string
		key             AbsoluteBucketKey
		bucketsPerCycle int64
		want            CycleBucketKey
		expectPanic     bool
	}{
		{
			name: "absolute_bucket_index_less_than_cycle",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 5,
			},
			bucketsPerCycle: 10,
			want: CycleBucketKey{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				BucketIndex:   5,
			},
		},
		{
			name: "absolute_bucket_index_greater_than_cycle",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 12,
			},
			bucketsPerCycle: 10,
			want: CycleBucketKey{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				BucketIndex:   2,
			},
		},
		{
			name: "index_multiple_of_cycle",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 20,
			},
			bucketsPerCycle: 10,
			want: CycleBucketKey{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				BucketIndex:   0,
			},
		},
		{
			name: "zero_index",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 0,
			},
			bucketsPerCycle: 10,
			want: CycleBucketKey{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				BucketIndex:   0,
			},
		},
		{
			name: "zero_cycle_panic",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 5,
			},
			bucketsPerCycle: 0,
			expectPanic:     true,
		},
		{
			name: "negative_cycle_panic",
			key: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 5,
			},
			bucketsPerCycle: -5,
			expectPanic:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("expected panic, but did not panic")
					}
				}()
			}
			got := test.key.ToCycleBucketKey(test.bucketsPerCycle)
			if !test.expectPanic && got != test.want {
				t.Errorf("%v.ToCycleBucketKey(%d) = %v, want %v", test.key, test.bucketsPerCycle, got, test.want)
			}
		})
	}
}

func TestGetAbsoluteBucketKey(t *testing.T) {
	tests := []struct {
		name           string
		feature        ExtractedLogFeatures
		bucketDuration time.Duration
		want           AbsoluteBucketKey
		expectPanic    bool
	}{
		{
			name: "happy_path_exact_division",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   1000,
			},
			bucketDuration: 100 * time.Millisecond,
			want: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 10,
			},
		},
		{
			name: "happy_path_with_remainder",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   1050,
			},
			bucketDuration: 100 * time.Millisecond,
			want: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 10,
			},
		},
		{
			name: "edge_case_zero_duration",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   1000,
			},
			bucketDuration: 0,
			expectPanic:    true,
		},
		{
			name: "edge_case_negative_duration",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   1000,
			},
			bucketDuration: -10 * time.Millisecond,
			expectPanic:    true,
		},
		{
			name: "edge_case_sub_millisecond_duration",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   1000,
			},
			bucketDuration: 500 * time.Microsecond,
			expectPanic:    true,
		},
		{
			name: "zero_timestamp",
			feature: ExtractedLogFeatures{
				DbUser:        "user1",
				QueryTemplate: "SELECT",
				TimestampMs:   0,
			},
			bucketDuration: 100 * time.Millisecond,
			want: AbsoluteBucketKey{
				DbUser:              "user1",
				QueryTemplate:       "SELECT",
				AbsoluteBucketIndex: 0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("expected panic, but did not panic")
					}
				}()
			}
			got := GetAbsoluteBucketKey(test.feature, test.bucketDuration)
			if !test.expectPanic && got != test.want {
				t.Errorf("GetAbsoluteBucketKey(%v, %v) = %v, want %v", test.feature, test.bucketDuration, got, test.want)
			}
		})
	}
}

func TestValidateDurations(t *testing.T) {
	tests := []struct {
		name           string
		bucketDuration time.Duration
		cycleDuration  time.Duration
		want           int64
		wantErr        bool
	}{
		{
			name:           "valid_weekly",
			bucketDuration: 1 * time.Hour,
			cycleDuration:  168 * time.Hour,
			want:           168,
			wantErr:        false,
		},
		{
			name:           "valid_daily",
			bucketDuration: 10 * time.Minute,
			cycleDuration:  24 * time.Hour,
			want:           144,
			wantErr:        false,
		},
		{
			name:           "zero_bucket_duration",
			bucketDuration: 0,
			cycleDuration:  24 * time.Hour,
			wantErr:        true,
		},
		{
			name:           "negative_bucket_duration",
			bucketDuration: -1 * time.Hour,
			cycleDuration:  24 * time.Hour,
			wantErr:        true,
		},
		{
			name:           "sub_millisecond_bucket_duration",
			bucketDuration: 500 * time.Microsecond,
			cycleDuration:  24 * time.Hour,
			wantErr:        true,
		},
		{
			name:           "zero_cycle_duration",
			bucketDuration: 1 * time.Hour,
			cycleDuration:  0,
			wantErr:        true,
		},
		{
			name:           "negative_cycle_duration",
			bucketDuration: 1 * time.Hour,
			cycleDuration:  -24 * time.Hour,
			wantErr:        true,
		},
		{
			name:           "bucket_duration_exceeds_cycle",
			bucketDuration: 25 * time.Hour,
			cycleDuration:  24 * time.Hour,
			wantErr:        true,
		},
		{
			name:           "indivisible_durations",
			bucketDuration: 7 * time.Hour,
			cycleDuration:  24 * time.Hour,
			wantErr:        true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidateDurations(test.bucketDuration, test.cycleDuration)
			if (err != nil) != test.wantErr {
				t.Errorf("ValidateDurations(%v, %v) error = %v, wantErr %v", test.bucketDuration, test.cycleDuration, err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Errorf("ValidateDurations(%v, %v) got %v, want %v", test.bucketDuration, test.cycleDuration, got, test.want)
			}
		})
	}
}
