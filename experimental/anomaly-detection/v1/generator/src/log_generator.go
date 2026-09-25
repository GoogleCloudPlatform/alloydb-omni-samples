package main

import (
	"fmt"
	"io"
	"math/rand"

	"google.golang.org/protobuf/encoding/protodelim"

	genpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
)

const (
	constUser   = "db_user"
	constDbName = "db_name"
)

// LogGenerator constructs AuditLogEntry protobufs.
type LogGenerator struct {
	rng *rand.Rand
}

// NewLogGenerator initializes a LogGenerator with a random source.
func NewLogGenerator(rng *rand.Rand) *LogGenerator {
	return &LogGenerator{
		rng: rng,
	}
}

// GenerateLog builds a query statement, constructs the AuditLogEntry protobuf, and streams it to the writer.
func (g *LogGenerator) GenerateLog(cfg *genpb.WeightedQueryTemplateConfig, writer io.Writer) error {
	statement := g.buildQueryStatement(cfg.GetTemplate())

	entry := &genpb.AuditLogEntry{}
	entry.SetNormalizedQueryTemplate(cfg.GetTemplate().GetNormalizedQueryTemplate())
	entry.SetQueryStatement(statement)
	entry.SetDbUser(constUser)
	entry.SetDbName(constDbName)
	entry.SetIsGroundTruthAnomalous(cfg.GetTemplate().GetIsGroundTruthAnomalous())

	_, err := protodelim.MarshalTo(writer, entry)
	return err
}

// BuildBucketLog constructs an AuditLogEntry protobuf with custom timestamp and anomaly label.
func (g *LogGenerator) BuildBucketLog(cfg *genpb.BucketQueryTemplateConfig, isSpike bool, timestampMs int64) *genpb.AuditLogEntry {
	statement := g.buildQueryStatement(cfg.GetTemplate())
	entry := &genpb.AuditLogEntry{}
	entry.SetNormalizedQueryTemplate(cfg.GetTemplate().GetNormalizedQueryTemplate())
	entry.SetQueryStatement(statement)

	dbUser := cfg.GetBucketConfig().GetDbUser()
	if dbUser == "" {
		dbUser = constUser
	}
	entry.SetDbUser(dbUser)
	entry.SetDbName(constDbName)
	entry.SetIsGroundTruthAnomalous(isSpike)
	entry.SetTimestampMs(timestampMs)

	return entry
}

// buildQueryStatement injects randomized values into placeholders (%v) to create the query statement.
func (g *LogGenerator) buildQueryStatement(cfg *genpb.QueryTemplateConfig) string {
	var vals []any
	for _, v := range cfg.GetPlaceholderValues() {
		if intSpec := v.GetIntPlaceholder(); intSpec != nil {
			vals = append(vals, intSpec.GetMin()+g.rng.Int31n(intSpec.GetMax()-intSpec.GetMin()+1))
		} else if floatSpec := v.GetFloatPlaceholder(); floatSpec != nil {
			vals = append(vals, floatSpec.GetMin()+g.rng.Float64()*(floatSpec.GetMax()-floatSpec.GetMin()))
		} else if stringSpec := v.GetStringPlaceholder(); stringSpec != nil {
			allowed := stringSpec.GetAllowedValues()
			if len(allowed) > 0 {
				idx := g.rng.Intn(len(allowed))
				vals = append(vals, allowed[idx])
			} else {
				vals = append(vals, "")
			}
		}
	}
	return fmt.Sprintf(cfg.GetNormalizedQueryTemplate(), vals...)
}
