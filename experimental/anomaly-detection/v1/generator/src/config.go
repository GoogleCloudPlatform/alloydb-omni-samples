// Package main implements config loading logic for the generator.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	cfgpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

// loadConfigs reads and validates the query template configurations from a textproto file.
func loadConfigs(ctx context.Context, filePath string) (*cfgpb.QueryTemplateConfigs, error) {
	// 1. Read config textproto
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	// 2. Create an empty instance of the Go struct defined by config proto
	configs := &cfgpb.QueryTemplateConfigs{}

	// 3. Unmarshal config textproto data to configs
	err = prototext.Unmarshal(data, configs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal textproto: %w", err)
	}

	// 4. Validate configurations
	if err := validateConfigs(configs); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return configs, nil
}

func isFiniteNonNegative(v float64) bool {
	return v >= 0.0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// validateConfigs ensures logical integrity of the loaded configurations.
func validateConfigs(configs *cfgpb.QueryTemplateConfigs) error {
	// Identify scenario: Weighted or Bucket sampling.
	if configs.GetWeightedSampling() != nil {
		cfgs := configs.GetWeightedSampling().GetQueryTemplateConfigs()
		if len(cfgs) == 0 {
			return fmt.Errorf("configuration contains no query templates")
		}
		var totalWeight float64
		for i, wCfg := range cfgs {
			if math.IsNaN(wCfg.GetProbabilityWeight()) {
				return fmt.Errorf("config[%d]: probability_weight cannot be NaN", i)
			}
			if wCfg.GetProbabilityWeight() < 0.0 {
				return fmt.Errorf("config[%d]: probability_weight cannot be negative", i)
			}
			totalWeight += wCfg.GetProbabilityWeight()

			if err := validateQueryTemplate(i, wCfg.GetTemplate()); err != nil {
				return err
			}
		}

		if totalWeight == 0 {
			return fmt.Errorf("at least one template must have a positive weight")
		}
		if math.IsInf(totalWeight, 1) {
			return fmt.Errorf("total probability weight exceeds maximum representable float64 value")
		}

	} else if configs.GetBucketSampling() != nil {
		cfgs := configs.GetBucketSampling().GetQueryTemplateConfigs()
		if len(cfgs) == 0 {
			return fmt.Errorf("configuration contains no query templates")
		}
		hasPositiveMean := false
		for i, bCfg := range cfgs {
			simCfg := bCfg.GetBucketConfig()
			if simCfg == nil {
				return fmt.Errorf("config[%d]: bucket_config cannot be nil under bucket_sampling scenario", i)
			}
			if !isFiniteNonNegative(simCfg.GetQueriesPerBucketMean()) {
				return fmt.Errorf("config[%d]: queries_per_bucket_mean must be finite and non-negative", i)
			}
			if !isFiniteNonNegative(simCfg.GetQueriesVariationStdDev()) {
				return fmt.Errorf("config[%d]: queries_variation_std_dev must be finite and non-negative", i)
			}
			if simCfg.GetAnomalousSpikeProbability() < 0.0 || simCfg.GetAnomalousSpikeProbability() > 1.0 || math.IsNaN(simCfg.GetAnomalousSpikeProbability()) {
				return fmt.Errorf("config[%d]: anomalous_spike_probability must be between 0.0 and 1.0", i)
			}
			if !isFiniteNonNegative(simCfg.GetAnomalousSpikeMultiplier()) {
				return fmt.Errorf("config[%d]: anomalous_spike_multiplier must be finite and non-negative", i)
			}
			if !isFiniteNonNegative(simCfg.GetWeekendTrafficMultiplier()) {
				return fmt.Errorf("config[%d]: weekend_traffic_multiplier must be finite and non-negative", i)
			}
			if simCfg.GetQueriesPerBucketMean() > 0 {
				hasPositiveMean = true
			}

			if err := validatePeakWindows(i, simCfg.GetPeakWindows()); err != nil {
				return err
			}

			if err := validateQueryTemplate(i, bCfg.GetTemplate()); err != nil {
				return err
			}
		}

		if !hasPositiveMean {
			return fmt.Errorf("at least one template must have a queries_per_bucket_mean > 0 for bucket_sampling")
		}

	} else {
		return fmt.Errorf("configuration must specify either weighted_sampling or bucket_sampling")
	}

	return nil
}

func validatePeakWindows(i int, windows []*cfgpb.PeakWindow) error {
	for wIdx, pw := range windows {
		if pw.GetHourStart() < 0 || pw.GetHourStart() > 23 {
			return fmt.Errorf("config[%d].peak_windows[%d]: hour_start (%d) must be in [0, 23]", i, wIdx, pw.GetHourStart())
		}
		if pw.GetHourEnd() < 0 || pw.GetHourEnd() > 23 {
			return fmt.Errorf("config[%d].peak_windows[%d]: hour_end (%d) must be in [0, 23]", i, wIdx, pw.GetHourEnd())
		}
		if !isFiniteNonNegative(pw.GetMultiplier()) {
			return fmt.Errorf("config[%d].peak_windows[%d]: multiplier must be finite and non-negative", i, wIdx)
		}
		if pw.GetMultiplier() > 0 && pw.GetHourStart() == pw.GetHourEnd() {
			return fmt.Errorf("config[%d].peak_windows[%d]: hour_start (%d) and hour_end (%d) cannot be equal when multiplier is active", i, wIdx, pw.GetHourStart(), pw.GetHourEnd())
		}
	}

	for w1 := 0; w1 < len(windows); w1++ {
		for w2 := w1 + 1; w2 < len(windows); w2++ {
			pw1 := windows[w1]
			pw2 := windows[w2]
			if pw1.GetMultiplier() <= 0 || pw2.GetMultiplier() <= 0 {
				continue
			}
			for h := 0; h < 24; h++ {
				if InPeakWindow(h, pw1) && InPeakWindow(h, pw2) {
					return fmt.Errorf("config[%d].peak_windows: peak window %d (%s) and peak window %d (%s) overlap at hour %d", i, w1, pw1.GetWindowName(), w2, pw2.GetWindowName(), h)
				}
			}
		}
	}
	return nil
}

func validateQueryTemplate(i int, cfg *cfgpb.QueryTemplateConfig) error {
	if cfg == nil || len(cfg.GetNormalizedQueryTemplate()) == 0 {
		return fmt.Errorf("config[%d]: normalized_query_template cannot be empty", i)
	}
	// The number of placeholder_values matches the number of %v placeholders in the normalized_query_template.
	expectedCount := strings.Count(cfg.GetNormalizedQueryTemplate(), "%v")
	actualCount := len(cfg.GetPlaceholderValues())
	if expectedCount != actualCount {
		return fmt.Errorf("config[%d]: placeholder count mismatch: template requires %d '%%v' placeholders, but got %d placeholder specs", i, expectedCount, actualCount)
	}

	// min <= max for all placeholder value ranges.
	for j, spec := range cfg.GetPlaceholderValues() {
		if intSpec := spec.GetIntPlaceholder(); intSpec != nil {
			if intSpec.GetMin() > intSpec.GetMax() {
				return fmt.Errorf("config[%d].placeholder_values[%d]: int min (%d) cannot be greater than max (%d)", i, j, intSpec.GetMin(), intSpec.GetMax())
			}
		} else if floatSpec := spec.GetFloatPlaceholder(); floatSpec != nil {
			if floatSpec.GetMin() > floatSpec.GetMax() {
				return fmt.Errorf("config[%d].placeholder_values[%d]: float min (%f) cannot be greater than max (%f)", i, j, floatSpec.GetMin(), floatSpec.GetMax())
			}
		} else if stringSpec := spec.GetStringPlaceholder(); stringSpec != nil {
			if len(stringSpec.GetAllowedValues()) == 0 {
				return fmt.Errorf("config[%d].placeholder_values[%d]: string_placeholder allowed_values cannot be empty", i, j)
			}
		} else {
			return fmt.Errorf("config[%d].placeholder_values[%d]: placeholder type must be specified", i, j)
		}
	}
	return nil
}

// InPeakWindow checks whether the given hour (0-23) falls within the peak window [hour_start, hour_end).
func InPeakWindow(hour int, pw *cfgpb.PeakWindow) bool {
	if pw.GetMultiplier() <= 0 {
		return false
	}
	start := int(pw.GetHourStart())
	end := int(pw.GetHourEnd())
	span := (end - start + 24) % 24
	if span == 0 {
		return false
	}
	offset := (hour - start + 24) % 24
	return offset < span
}
