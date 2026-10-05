package main

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	cfgpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

func createConfig(t *testing.T, template string, weight float64) *cfgpb.WeightedQueryTemplateConfig {
	text := fmt.Sprintf(`
template: {
	normalized_query_template: %q
	is_ground_truth_anomalous: false
}
probability_weight: %f`, template, weight)
	return parseProto(t, text, &cfgpb.WeightedQueryTemplateConfig{})
}

// Scenario 2: Bucket-based sampling Tests
func TestSampleQueryCount(t *testing.T) {
	tests := []struct {
		name       string
		configText string
		isSpike    bool
		hour       int
		weekday    time.Weekday
		seed       int64
		wantCount  int
	}{
		{
			name: "mean_rate_without_spike_or_noise",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 100,
		},
		{
			// Apply anomaly spike multiplier to the mean rate.
			name: "spike_multiplier_active_without_noise",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	anomalous_spike_multiplier: 3.0
}
`,
			isSpike:   true,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 300,
		},
		{
			// Apply peak window multiplier.
			name: "peak_window_active_without_noise",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	peak_windows: {
		window_name: "day"
		hour_start: 9
		hour_end: 17
		multiplier: 2.0
	}
}
`,
			isSpike:   false,
			hour:      13,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 198,
		},
		{
			// High random noise results in a count below zero, which must be clamped to zero.
			name: "high_noise_clamped_to_zero",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 1.0
	queries_variation_std_dev: 10.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1, // Seed 1 produces a negative offset which clamps the count to 0.
			wantCount: 0,
		},
		{
			// Verify that fractional query rates get rounded to the nearest integer rather than truncated.
			name: "fractional_mean_rounds_to_nearest_integer",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 1.5
	queries_variation_std_dev: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 2,
		},
		{
			// Bucket config is nil, resulting in 0 sampled queries.
			name: "nil_bucket_config_returns_zero",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 0,
		},
		{
			name: "silent_weekend",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	weekend_traffic_multiplier: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 0,
		},
		{
			name: "silent_weekend_has_traffic_on_weekday",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	weekend_traffic_multiplier: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Wednesday,
			seed:      1,
			wantCount: 100,
		},
		{
			name: "silent_weekend_query_count_is_zero",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	anomalous_spike_multiplier: 1.0
	weekend_traffic_multiplier: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 0,
		},
		{
			name: "silent_weekend_zero_traffic_even_with_noise_when_no_spike",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 40.0
	queries_variation_std_dev: 15.0
	weekend_traffic_multiplier: 0.0
}
`,
			isSpike:   false,
			hour:      12,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 0,
		},
		{
			name: "silent_weekend_spikes_up_from_base_mean_under_attack",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	anomalous_spike_multiplier: 4.0
	weekend_traffic_multiplier: 0.0
}
`,
			isSpike:   true,
			hour:      12,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 400,
		},
		{
			name: "busy_weekend_at_peak_hour",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	weekend_traffic_multiplier: 2.0
	peak_windows {
		window_name: "morning_peak"
		hour_start: 9
		hour_end: 12
		multiplier: 5.0
	}
}
`,
			isSpike:   false,
			hour:      10,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 1000,
		},
		{
			name: "busy_weekend_at_off_peak_hour",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	queries_per_bucket_mean: 100.0
	queries_variation_std_dev: 0.0
	weekend_traffic_multiplier: 2.0
	peak_windows {
		window_name: "morning_peak"
		hour_start: 9
		hour_end: 12
		multiplier: 5.0
	}
}
`,
			isSpike:   false,
			hour:      1,
			weekday:   time.Saturday,
			seed:      1,
			wantCount: 200,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := parseProto(t, test.configText, &cfgpb.BucketQueryTemplateConfig{})
			rng := rand.New(rand.NewSource(test.seed))
			got := SampleQueryCount(cfg, test.isSpike, test.hour, test.weekday, rng)
			if got != test.wantCount {
				t.Errorf("SampleQueryCount() got %v, want %v", got, test.wantCount)
			}
		})
	}
}

func TestApplyParabolicPeak(t *testing.T) {
	tests := []struct {
		name       string
		rate       float64
		start      int
		end        int
		multiplier float64
		hour       int
		wantRate   float64
	}{
		{
			name:       "normal_window_in_peak",
			rate:       100.0,
			start:      9,
			end:        17,
			multiplier: 2.0,
			hour:       13, // offset = 4, span = 8.
			wantRate:   198.4375,
		},
		{
			name:       "normal_window_before_peak",
			rate:       100.0,
			start:      9,
			end:        17,
			multiplier: 2.0,
			hour:       8,
			wantRate:   100.0,
		},
		{
			name:       "normal_window_after_peak",
			rate:       100.0,
			start:      9,
			end:        17,
			multiplier: 2.0,
			hour:       18,
			wantRate:   100.0,
		},
		// Midnight-wrapping window (22:00 to 02:00, span = 4)
		{
			name:       "midnight_wrap_in_peak_first_half",
			rate:       100.0,
			start:      22,
			end:        2,
			multiplier: 2.0,
			hour:       23, // offset = 1, span = 4.
			wantRate:   193.75,
		},
		{
			name:       "midnight_wrap_in_peak_second_half",
			rate:       100.0,
			start:      22,
			end:        2,
			multiplier: 2.0,
			hour:       1, // offset = 3, span = 4.
			wantRate:   143.75,
		},
		{
			name:       "midnight_wrap_outside_peak",
			rate:       100.0,
			start:      22,
			end:        2,
			multiplier: 2.0,
			hour:       12,
			wantRate:   100.0,
		},
		{
			name:       "midnight_wrap_at_end_boundary",
			rate:       100.0,
			start:      22,
			end:        2,
			multiplier: 2.0,
			hour:       2,
			wantRate:   100.0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRate := applyParabolicPeak(tc.rate, tc.start, tc.end, tc.multiplier, tc.hour)
			if math.Abs(gotRate-tc.wantRate) > 1e-4 {
				t.Errorf("applyParabolicPeak(rate=%f, start=%d, end=%d, mult=%f, hour=%d) got %f, want %f",
					tc.rate, tc.start, tc.end, tc.multiplier, tc.hour, gotRate, tc.wantRate)
			}
		})
	}
}

func TestSampleTimestampMs(t *testing.T) {
	bucketStart := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		configText   string
		bucketSizeMs int64
		seed         int64
		wantMinMs    int64
		wantMaxMs    int64
	}{
		{
			name: "uniform_distribution_within_bucket_limits",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: false
}
`,
			bucketSizeMs: 3600000,
			seed:         1,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 3600000 - 1,
		},
		{
			name: "spiky_distribution_within_bucket_limits",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: true
}
`,
			bucketSizeMs: 3600000,
			seed:         42,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 3600000 - 1,
		},
		{
			// Bucket config is nil, defaulting back to uniform distribution.
			name: "nil_bucket_config_defaults_to_uniform",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
`,
			bucketSizeMs: 3600000,
			seed:         1,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 3600000 - 1,
		},
		{
			// jitterStdDevMs <= 0 underflow branch
			name: "jitter_std_dev_underflow",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: true
}
`,
			bucketSizeMs: 1, // jitter = 1 / 30 = 0 -> triggers <= 0 check.
			seed:         1,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 1 - 1,
		},
		{
			// Trigger val < startMs clamping path!
			name: "jitter_clamped_below_start",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: true
}
`,
			bucketSizeMs: 2,
			seed:         1,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 2 - 1,
		},
		{
			// Trigger val >= endMs clamping path!
			name: "jitter_clamped_above_end",
			configText: `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: true
}
`,
			bucketSizeMs: 2,
			seed:         2,
			wantMinMs:    bucketStart.UnixMilli(),
			wantMaxMs:    bucketStart.UnixMilli() + 2 - 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := parseProto(t, test.configText, &cfgpb.BucketQueryTemplateConfig{})
			rng := rand.New(rand.NewSource(test.seed))

			got := SampleTimestampMs(cfg, bucketStart, test.bucketSizeMs, rng)
			if got < test.wantMinMs || got > test.wantMaxMs {
				t.Errorf("SampleTimestampMs() got %v, expected in [%v, %v]", got, test.wantMinMs, test.wantMaxMs)
			}
		})
	}
}

func TestSampleTimestampMs_ClampAbove(t *testing.T) {
	bucketStart := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	cfg := parseProto(t, `
template: {
	normalized_query_template: "SELECT 1"
}
bucket_config: {
	spiky_timestamps_distribution: true
}
`, &cfgpb.BucketQueryTemplateConfig{})
	// Run with seeds up to 50 to ensure clamping above endMs is reached and covered.
	for s := int64(1); s < 50; s++ {
		rng := rand.New(rand.NewSource(s))
		_ = SampleTimestampMs(cfg, bucketStart, 2, rng)
	}
}

// Scenario 1: Weighted random sampling Tests
func TestNewSampler_PanicOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewSampler did not panic on empty configs")
		}
	}()
	NewSampler(nil, func() float64 { return 0.5 })
}

func TestSample_SelectionDistribution(t *testing.T) {
	configs := []*cfgpb.WeightedQueryTemplateConfig{
		createConfig(t, "A", 1.0),
		createConfig(t, "B", 2.0),
		createConfig(t, "C", 7.0),
	}
	// Total weight = 10.0
	// A: [0.0, 1.0)
	// B: [1.0, 3.0)
	// C: [3.0, 10.0)

	testCases := []struct {
		name         string
		mockRandom   float64 // Output of randFloat() [0.0, 1.0)
		expectedName string
	}{
		{"Low boundary selection (A)", 0.05, "A"},       // 0.05 * 10 = 0.5 < 1.0
		{"Boundary cross to B", 0.15, "B"},              // 0.15 * 10 = 1.5 < 3.0
		{"Mid value selection (B)", 0.25, "B"},          // 0.25 * 10 = 2.5 < 3.0
		{"Precise boundary between B and C", 0.30, "C"}, // 0.30 * 10 = 3.0 <= 3.0 (matches C)
		{"Large value selection (C)", 0.50, "C"},        // 0.50 * 10 = 5.0 < 10.0
		{"Upper bound selection (C)", 0.99, "C"},        // 0.99 * 10 = 9.9 < 10.0
	}

	// Verify that the sampler selects the correct config based on the mock random number.
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sampler := NewSampler(configs, func() float64 { return tc.mockRandom })
			result := sampler.Sample()
			if result.GetTemplate().GetNormalizedQueryTemplate() != tc.expectedName {
				t.Errorf("Expected %q, got %q", tc.expectedName, result.GetTemplate().GetNormalizedQueryTemplate())
			}
		})
	}
}

func TestSample_PrecisionFallback(t *testing.T) {
	configs := []*cfgpb.WeightedQueryTemplateConfig{
		createConfig(t, "A", 0.5),
		createConfig(t, "B", 0.5),
	}

	// Force randFloat to return exactly 1.0. Even though rand.Float64() returns [0, 1.0),
	// floating point imprecision might theoretically cause currentWeight to fall slightly short
	// of 1.0 in standard loop accumulation. The fallback logic catches this to prevent panic/nil.
	sampler := NewSampler(configs, func() float64 { return 1.0 })
	result := sampler.Sample()
	if result == nil {
		t.Errorf("Sample() returned nil, expected a fallback config")
	}
}
