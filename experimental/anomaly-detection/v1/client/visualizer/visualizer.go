// Package visualizer generates timelines and waveforms plot for alloydb anomaly detection logs.
package visualizer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/prototext"

	auditpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/generator/proto"
	findpb "github.com/GoogleCloudPlatform/alloydb-omni-samples/experimental/anomaly-detection/v1/server/proto"
)

type dataPoint struct {
	Index        int
	TimePoint    time.Time
	Count        float64
	IsTraining   bool
	IsAnomaly    bool
	IsSilentSlot bool
}

type legendItem struct {
	label  string
	color  color.RGBA
	isLine bool
}

type horizontalLegend struct {
	entries []legendItem
}

// Plot draws the legend horizontally below the plot area.
func (hl horizontalLegend) Plot(c draw.Canvas, plt *plot.Plot) {
	sty := plt.Legend.TextStyle
	width := c.Max.X - c.Min.X

	var itemWidths []vg.Length
	var totalWidth vg.Length
	spacing := vg.Points(30)
	thumbWidth := vg.Points(20)

	for _, e := range hl.entries {
		w := thumbWidth + sty.Rectangle(" "+e.label).Max.X
		itemWidths = append(itemWidths, w)
		totalWidth += w
	}
	totalWidth += spacing * vg.Length(len(hl.entries)-1)

	startX := c.Min.X + (width-totalWidth)/2
	y := c.Min.Y - vg.Points(58)

	for i, e := range hl.entries {
		if e.isLine {
			var path vg.Path
			path.Move(vg.Point{X: startX, Y: y + vg.Points(4)})
			path.Line(vg.Point{X: startX + thumbWidth, Y: y + vg.Points(4)})

			c.SetColor(e.color)
			c.SetLineDash(nil, 0)
			c.SetLineWidth(vg.Points(2))
			c.Stroke(path)
		} else {
			var path vg.Path
			path.Move(vg.Point{X: startX + thumbWidth/2 - vg.Points(3), Y: y + vg.Points(1)})
			path.Line(vg.Point{X: startX + thumbWidth/2 + vg.Points(3), Y: y + vg.Points(1)})
			path.Line(vg.Point{X: startX + thumbWidth/2 + vg.Points(3), Y: y + vg.Points(7)})
			path.Line(vg.Point{X: startX + thumbWidth/2 - vg.Points(3), Y: y + vg.Points(7)})
			path.Close()

			c.SetColor(e.color)
			c.Fill(path)
		}

		c.SetLineDash(nil, 0)

		labelX := startX + thumbWidth + vg.Points(4)
		c.FillText(sty, vg.Point{X: labelX, Y: y}, e.label)

		startX += itemWidths[i] + spacing
	}
}

// GenerateTrafficWaveform reads logs.binproto from outDir, aggregates counts and plots the waveform.
func GenerateTrafficWaveform(ctx context.Context, outDir string, bucketSizeMin int, startTimeMs, trainEndTimeMs, endTimeMs int64) error {
	logsPath := filepath.Join(outDir, "logs.binproto")
	points, err := loadAndAggregateLogs(ctx, logsPath, bucketSizeMin, startTimeMs, trainEndTimeMs, endTimeMs)
	if err != nil {
		return err
	}

	startTime := time.UnixMilli(startTimeMs)
	trainEndTime := time.UnixMilli(trainEndTimeMs)
	endTime := time.UnixMilli(endTimeMs)

	p, err := buildTrafficWaveformPlot(points, bucketSizeMin, startTime, trainEndTime, endTime)
	if err != nil {
		return err
	}

	return saveAndSyncPlot(ctx, p, outDir, "raw_traffic_waveform.png", "Raw traffic waveform")
}

func loadAndAggregateLogs(ctx context.Context, logsPath string, bucketSizeMin int, startTimeMs, trainEndTimeMs, endTimeMs int64) ([]dataPoint, error) {
	file, err := os.Open(logsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open logs file: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)

	bucketSizeMs := int64(bucketSizeMin) * 60 * 1000
	startMs := startTimeMs
	bucketCounts := make(map[int]int)
	bucketIsAnomalous := make(map[int]bool)

	for {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(reader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to parse log entry: %w", err)
		}

		idx := int((entry.GetTimestampMs() - startMs) / bucketSizeMs)
		bucketCounts[idx]++
		if entry.GetIsGroundTruthAnomalous() {
			bucketIsAnomalous[idx] = true
		}
	}

	startTime := time.UnixMilli(startTimeMs)
	trainEndTime := time.UnixMilli(trainEndTimeMs)
	bucketSize := time.Duration(bucketSizeMin) * time.Minute

	totalSlots := int((endTimeMs - startTimeMs) / bucketSizeMs)
	var points []dataPoint
	for i := 0; i < totalSlots; i++ {
		t := startTime.Add(time.Duration(i) * bucketSize)
		isTraining := t.Before(trainEndTime)
		points = append(points, dataPoint{
			Index:      i,
			TimePoint:  t,
			Count:      float64(bucketCounts[i]),
			IsTraining: isTraining,
			IsAnomaly:  bucketIsAnomalous[i],
		})
	}
	return points, nil
}

func buildTrafficWaveformPlot(points []dataPoint, bucketSizeMin int, startTime, trainEndTime, endTime time.Time) (*plot.Plot, error) {
	p := plot.New()
	p.Title.Text = fmt.Sprintf("Raw Traffic Waveform Timeline (Bucket Size: %d min)", bucketSizeMin)
	p.X.Label.Text = "Time (Week Boundaries)"
	p.Y.Label.Text = "Bucket Query Count"

	bucketSize := time.Duration(bucketSizeMin) * time.Minute

	var trainXYs plotter.XYs
	var testXYs plotter.XYs
	var anomalyXYs plotter.XYs

	for _, pt := range points {
		xVal := float64(pt.Index)
		if pt.IsTraining {
			trainXYs = append(trainXYs, plotter.XY{X: xVal, Y: pt.Count})
		} else {
			testXYs = append(testXYs, plotter.XY{X: xVal, Y: pt.Count})
			if pt.IsAnomaly {
				anomalyXYs = append(anomalyXYs, plotter.XY{X: xVal, Y: pt.Count})
			}
		}
	}

	trainLine, err := plotter.NewLine(trainXYs)
	if err != nil {
		return nil, fmt.Errorf("failed to create train line: %w", err)
	}
	trainLine.Color = color.RGBA{R: 51, G: 102, B: 204, A: 255}
	trainLine.Width = vg.Points(0.2)
	p.Add(trainLine)

	if len(testXYs) > 0 {
		if len(trainXYs) > 0 {
			testXYs = append(plotter.XYs{trainXYs[len(trainXYs)-1]}, testXYs...)
		}
		testLine, err := plotter.NewLine(testXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create test line: %w", err)
		}
		testLine.Color = color.RGBA{R: 16, G: 150, B: 24, A: 255}
		testLine.Width = vg.Points(0.2)
		p.Add(testLine)
	}

	if len(anomalyXYs) > 0 {
		anomalyScatter, err := plotter.NewScatter(anomalyXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create anomaly scatter: %w", err)
		}
		anomalyScatter.Color = color.RGBA{R: 220, G: 57, B: 18, A: 255}
		anomalyScatter.Radius = vg.Points(1)
		anomalyScatter.Shape = draw.CircleGlyph{}
		p.Add(anomalyScatter)
	}

	legendEntries := []legendItem{
		{
			label:  "Training Logs (Normal)",
			color:  color.RGBA{R: 51, G: 102, B: 204, A: 255},
			isLine: true,
		},
		{
			label:  "Testing Logs",
			color:  color.RGBA{R: 16, G: 150, B: 24, A: 255},
			isLine: true,
		},
		{
			label:  "Ground Truth Anomalies",
			color:  color.RGBA{R: 220, G: 57, B: 18, A: 255},
			isLine: false,
		},
	}
	p.Add(horizontalLegend{entries: legendEntries})
	p.X.Label.Padding = vg.Points(35)

	var ticks []plot.Tick
	slotsPerWeek := int((7 * 24 * time.Hour) / bucketSize)
	trainWeeks := int(trainEndTime.Sub(startTime) / (7 * 24 * time.Hour))
	testWeeks := int(endTime.Sub(trainEndTime) / (7 * 24 * time.Hour))
	for w := 0; w <= trainWeeks+testWeeks; w++ {
		val := w * slotsPerWeek
		label := fmt.Sprintf("Week %d", w)
		if w == 0 {
			label = "Start"
		} else if w == trainWeeks {
			label = "Train End / Test Start"
		}
		ticks = append(ticks, plot.Tick{
			Value: float64(val),
			Label: label,
		})
	}
	p.X.Tick.Marker = plot.ConstantTicks(ticks)

	return p, nil
}

func saveAndSyncPlot(ctx context.Context, p *plot.Plot, outDir, filename, displayName string) error {
	outputPath := filepath.Join(outDir, filename)
	if err := p.Save(22*vg.Inch, 6*vg.Inch, outputPath); err != nil {
		return fmt.Errorf("failed to save %s: %w", displayName, err)
	}
	fmt.Printf("%s saved to temp: file://%s\n", displayName, outputPath)

	if wDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); wDir != "" {
		persistentPath := filepath.Join(wDir, "experimental/anomaly-detection/v1/client", filename)
		input, err := os.ReadFile(outputPath)
		if err == nil {
			if err := os.WriteFile(persistentPath, input, 0644); err != nil {
				log.Printf("failed to copy %s to workspace: %v", displayName, err)
			} else {
				fmt.Printf("Copied %s to workspace: file://%s\n", displayName, persistentPath)
			}
		}
	}
	return nil
}

// GeneratePredictionWaveform reads logs, maps them, parses findings from findingsPath, and plots the prediction results.
func GeneratePredictionWaveform(ctx context.Context, logsPath, findingsPath, outDir string, bucketSizeMin int, startTimeMs, trainEndTimeMs, endTimeMs int64, silentThresholdRatio float64) error {
	points, err := loadAndAggregatePredictions(ctx, logsPath, findingsPath, bucketSizeMin, startTimeMs, trainEndTimeMs, endTimeMs, silentThresholdRatio)
	if err != nil {
		return err
	}

	startTime := time.UnixMilli(startTimeMs)
	trainEndTime := time.UnixMilli(trainEndTimeMs)
	endTime := time.UnixMilli(endTimeMs)

	p, err := buildPredictionWaveformPlot(points, bucketSizeMin, startTime, trainEndTime, endTime)
	if err != nil {
		return err
	}

	return saveAndSyncPlot(ctx, p, outDir, "prediction_waveform_s3.png", "Model prediction waveform")
}

func loadAndAggregatePredictions(ctx context.Context, logsPath, findingsPath string, bucketSizeMin int, startTimeMs, trainEndTimeMs, endTimeMs int64, silentThresholdRatio float64) ([]dataPoint, error) {
	logFile, err := os.Open(logsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open logs file: %w", err)
	}
	defer logFile.Close()
	logReader := bufio.NewReader(logFile)

	findingsFile, err := os.Open(findingsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open findings file: %w", err)
	}
	defer findingsFile.Close()
	findingsScanner := bufio.NewScanner(findingsFile)

	bucketSizeMs := int64(bucketSizeMin) * 60 * 1000
	startMs := startTimeMs
	bucketCounts := make(map[int]int)
	bucketIsAnomalousPredict := make(map[int]bool)

	for {
		entry := &auditpb.AuditLogEntry{}
		err := protodelim.UnmarshalFrom(logReader, entry)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("failed to parse log entry: %w", err)
		}

		idx := int((entry.GetTimestampMs() - startMs) / bucketSizeMs)
		bucketCounts[idx]++

		// If this is a testing log (timestamp >= trainEndTimeMs), read the corresponding prediction
		if entry.GetTimestampMs() >= trainEndTimeMs {
			if !findingsScanner.Scan() {
				if err := findingsScanner.Err(); err != nil {
					return nil, fmt.Errorf("failed to scan findings: %w", err)
				}
				return nil, fmt.Errorf("unexpected end of findings stream: fewer findings than test logs")
			}
			line := findingsScanner.Text()
			finding := &findpb.AnomalyFinding{}
			if err := prototext.Unmarshal([]byte(line), finding); err != nil {
				return nil, fmt.Errorf("failed to unmarshal finding textproto: %w", err)
			}
			if finding.GetIsAnomalousPredict() {
				bucketIsAnomalousPredict[idx] = true
			}
		}
	}

	if findingsScanner.Scan() {
		return nil, fmt.Errorf("unexpected extra findings: more findings than test logs")
	}
	if err := findingsScanner.Err(); err != nil {
		return nil, err
	}

	startTime := time.UnixMilli(startTimeMs)
	trainEndTime := time.UnixMilli(trainEndTimeMs)
	bucketSize := time.Duration(bucketSizeMin) * time.Minute

	totalSlots := int((endTimeMs - startTimeMs) / bucketSizeMs)

	// Calculate totalCount during the training phase.
	var totalCount float64
	for i := 0; i < totalSlots; i++ {
		t := startTime.Add(time.Duration(i) * bucketSize)
		if t.Before(trainEndTime) {
			totalCount += float64(bucketCounts[i])
		}
	}

	vizRatio := silentThresholdRatio
	if vizRatio < 0.005 {
		vizRatio = 0.005
	}
	limit := totalCount * vizRatio

	// Calculate training counts for weekly slots to identify silent slots.
	slotsPerWeek := int((7 * 24 * time.Hour) / bucketSize)
	weeklySlotCounts := make([]float64, slotsPerWeek)
	startTimeIndex := startTimeMs / bucketSizeMs
	for i := 0; i < totalSlots; i++ {
		t := startTime.Add(time.Duration(i) * bucketSize)
		if t.Before(trainEndTime) {
			slotIdx := (startTimeIndex + int64(i)) % int64(slotsPerWeek)
			weeklySlotCounts[slotIdx] += float64(bucketCounts[i])
		}
	}

	isSilentSlot := make([]bool, slotsPerWeek)
	for idx := 0; idx < slotsPerWeek; idx++ {
		if weeklySlotCounts[idx] < limit {
			isSilentSlot[idx] = true
		}
	}

	var points []dataPoint
	for i := 0; i < totalSlots; i++ {
		t := startTime.Add(time.Duration(i) * bucketSize)
		isTraining := t.Before(trainEndTime)
		slotIdx := (startTimeIndex + int64(i)) % int64(slotsPerWeek)

		points = append(points, dataPoint{
			Index:        i,
			TimePoint:    t,
			Count:        float64(bucketCounts[i]),
			IsTraining:   isTraining,
			IsAnomaly:    bucketIsAnomalousPredict[i],
			IsSilentSlot: isSilentSlot[slotIdx],
		})
	}
	return points, nil
}

func buildPredictionWaveformPlot(points []dataPoint, bucketSizeMin int, startTime, trainEndTime, endTime time.Time) (*plot.Plot, error) {
	p := plot.New()
	p.Title.Text = fmt.Sprintf("Testing Phase Traffic Waveform (Bucket Size: %d min)", bucketSizeMin)
	p.X.Label.Text = "Time (Week Boundaries)"
	p.Y.Label.Text = "Bucket Query Count"

	bucketSize := time.Duration(bucketSizeMin) * time.Minute

	var testXYs plotter.XYs
	var anomalyXYs plotter.XYs
	var yMax float64

	for _, pt := range points {
		xVal := float64(pt.Index)
		if !pt.IsTraining {
			testXYs = append(testXYs, plotter.XY{X: xVal, Y: pt.Count})
			if pt.Count > yMax {
				yMax = pt.Count
			}
			if pt.IsAnomaly {
				anomalyXYs = append(anomalyXYs, plotter.XY{X: xVal, Y: pt.Count})
			}
		}
	}

	if yMax == 0.0 {
		yMax = 40.0
	}

	// Find contiguous ranges of silent slots in the testing phase using a state tracker.
	type rangeType int
	const (
		typeNone rangeType = iota
		typeWeekend
		typeLunch
		typeNight
	)

	var weekendRanges [][]int
	var lunchRanges [][]int
	var nightRanges [][]int

	var currentRange []int
	currentKind := typeNone

	saveCurrentRange := func() {
		if len(currentRange) == 0 {
			return
		}
		switch currentKind {
		case typeWeekend:
			weekendRanges = append(weekendRanges, currentRange)
		case typeLunch:
			lunchRanges = append(lunchRanges, currentRange)
		case typeNight:
			nightRanges = append(nightRanges, currentRange)
		}
		currentRange = nil
		currentKind = typeNone
	}

	for _, pt := range points {
		kind := typeNone
		if !pt.IsTraining && pt.IsSilentSlot {
			isWeekend := pt.TimePoint.Weekday() == time.Saturday || pt.TimePoint.Weekday() == time.Sunday
			if isWeekend {
				kind = typeWeekend
			} else if pt.TimePoint.Hour() == 12 {
				kind = typeLunch
			} else {
				kind = typeNight
			}
		}

		if kind != currentKind {
			saveCurrentRange()
			currentKind = kind
		}
		if kind != typeNone {
			currentRange = append(currentRange, pt.Index)
		}
	}
	saveCurrentRange()

	// Helper function to draw background shading polygons.
	drawShading := func(ranges [][]int, colorVal color.RGBA) {
		for _, r := range ranges {
			startIdx := float64(r[0]) - 0.5
			endIdx := float64(r[len(r)-1]) + 0.5

			polyPoints := plotter.XYs{
				{X: startIdx, Y: 0},
				{X: endIdx, Y: 0},
				{X: endIdx, Y: yMax * 1.15},
				{X: startIdx, Y: yMax * 1.15},
			}

			poly, err := plotter.NewPolygon(polyPoints)
			if err == nil {
				poly.Color = colorVal
				poly.LineStyle.Width = 0 // Remove border line
				p.Add(poly)
			}
		}
	}

	drawShading(weekendRanges, color.RGBA{R: 241, G: 243, B: 244, A: 255})
	drawShading(lunchRanges, color.RGBA{R: 254, G: 247, B: 224, A: 255})
	drawShading(nightRanges, color.RGBA{R: 238, G: 240, B: 252, A: 255})

	if len(testXYs) > 0 {
		testLine, err := plotter.NewLine(testXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create test line: %w", err)
		}
		testLine.Color = color.RGBA{R: 16, G: 150, B: 24, A: 255}
		testLine.Width = vg.Points(0.8)
		p.Add(testLine)
	}

	if len(anomalyXYs) > 0 {
		anomalyScatter, err := plotter.NewScatter(anomalyXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create anomaly scatter: %w", err)
		}
		anomalyScatter.Color = color.RGBA{R: 220, G: 57, B: 18, A: 255}
		anomalyScatter.Radius = vg.Points(2)
		anomalyScatter.Shape = draw.CircleGlyph{}
		p.Add(anomalyScatter)
	}

	legendEntries := []legendItem{
		{
			label:  "Testing Logs (Normal/Active)",
			color:  color.RGBA{R: 16, G: 150, B: 24, A: 255},
			isLine: true,
		},
		{
			label:  "Predicted Anomalies",
			color:  color.RGBA{R: 220, G: 57, B: 18, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Lunchtime (Background)",
			color:  color.RGBA{R: 254, G: 247, B: 224, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Nighttime (Background)",
			color:  color.RGBA{R: 238, G: 240, B: 252, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Weekend (Background)",
			color:  color.RGBA{R: 241, G: 243, B: 244, A: 255},
			isLine: false,
		},
	}
	p.Add(horizontalLegend{entries: legendEntries})
	p.X.Label.Padding = vg.Points(35)

	var ticks []plot.Tick
	slotsPerWeek := int((7 * 24 * time.Hour) / bucketSize)
	trainWeeks := int(trainEndTime.Sub(startTime) / (7 * 24 * time.Hour))
	testWeeks := int(endTime.Sub(trainEndTime) / (7 * 24 * time.Hour))
	for w := trainWeeks; w <= trainWeeks+testWeeks; w++ {
		val := w * slotsPerWeek
		label := fmt.Sprintf("Week %d", w)
		if w == trainWeeks {
			label = "Test Start"
		}
		ticks = append(ticks, plot.Tick{
			Value: float64(val),
			Label: label,
		})
	}
	p.X.Tick.Marker = plot.ConstantTicks(ticks)

	return p, nil
}

// GenerateTestPhaseTrafficWaveform plots ONLY the testing phase traffic waveform with Ground Truth anomalies and background shading.
func GenerateTestPhaseTrafficWaveform(ctx context.Context, logsPath, outDir, filename string, bucketSizeMin int, startTimeMs, trainEndTimeMs, endTimeMs int64, silentThresholdRatio float64) error {
	points, err := loadAndAggregateLogs(ctx, logsPath, bucketSizeMin, startTimeMs, trainEndTimeMs, endTimeMs)
	if err != nil {
		return err
	}

	startTime := time.UnixMilli(startTimeMs)
	trainEndTime := time.UnixMilli(trainEndTimeMs)
	endTime := time.UnixMilli(endTimeMs)

	p, err := buildTestPhaseTrafficWaveformPlot(points, bucketSizeMin, startTime, trainEndTime, endTime, silentThresholdRatio)
	if err != nil {
		return err
	}

	return saveAndSyncPlot(ctx, p, outDir, filename, "Raw traffic waveform (Test Phase)")
}

func buildTestPhaseTrafficWaveformPlot(points []dataPoint, bucketSizeMin int, startTime, trainEndTime, endTime time.Time, silentThresholdRatio float64) (*plot.Plot, error) {
	p := plot.New()
	p.Title.Text = fmt.Sprintf("Testing Phase Traffic Waveform - Ground Truth (Bucket Size: %d min)", bucketSizeMin)
	p.X.Label.Text = "Time (Week Boundaries)"
	p.Y.Label.Text = "Bucket Query Count"

	bucketSize := time.Duration(bucketSizeMin) * time.Minute

	var testXYs plotter.XYs
	var anomalyXYs plotter.XYs
	var yMax float64

	for _, pt := range points {
		xVal := float64(pt.Index)
		if !pt.IsTraining {
			testXYs = append(testXYs, plotter.XY{X: xVal, Y: pt.Count})
			if pt.Count > yMax {
				yMax = pt.Count
			}
			if pt.IsAnomaly {
				anomalyXYs = append(anomalyXYs, plotter.XY{X: xVal, Y: pt.Count})
			}
		}
	}

	if yMax == 0.0 {
		yMax = 40.0
	}

	// Calculate totalCount during the training phase for silent slots detection.
	totalSlots := len(points)
	var totalCount float64
	for i := 0; i < totalSlots; i++ {
		if points[i].IsTraining {
			totalCount += points[i].Count
		}
	}

	vizRatio := silentThresholdRatio
	if vizRatio < 0.005 {
		vizRatio = 0.005
	}
	limit := totalCount * vizRatio

	slotsPerWeek := int((7 * 24 * time.Hour) / bucketSize)
	weeklySlotCounts := make([]float64, slotsPerWeek)
	startTimeIndex := startTime.UnixMilli() / (int64(bucketSizeMin) * 60 * 1000)
	for i := 0; i < totalSlots; i++ {
		if points[i].IsTraining {
			slotIdx := (startTimeIndex + int64(i)) % int64(slotsPerWeek)
			weeklySlotCounts[slotIdx] += points[i].Count
		}
	}

	isSilentSlot := make([]bool, slotsPerWeek)
	for idx := 0; idx < slotsPerWeek; idx++ {
		if weeklySlotCounts[idx] < limit {
			isSilentSlot[idx] = true
		}
	}

	type rangeType int
	const (
		typeNone rangeType = iota
		typeWeekend
		typeLunch
		typeNight
	)

	var weekendRanges [][]int
	var lunchRanges [][]int
	var nightRanges [][]int

	var currentRange []int
	currentKind := typeNone

	saveCurrentRange := func() {
		if len(currentRange) == 0 {
			return
		}
		switch currentKind {
		case typeWeekend:
			weekendRanges = append(weekendRanges, currentRange)
		case typeLunch:
			lunchRanges = append(lunchRanges, currentRange)
		case typeNight:
			nightRanges = append(nightRanges, currentRange)
		}
		currentRange = nil
		currentKind = typeNone
	}

	for _, pt := range points {
		kind := typeNone
		slotIdx := (startTimeIndex + int64(pt.Index)) % int64(slotsPerWeek)
		if !pt.IsTraining && isSilentSlot[slotIdx] {
			isWeekend := pt.TimePoint.Weekday() == time.Saturday || pt.TimePoint.Weekday() == time.Sunday
			if isWeekend {
				kind = typeWeekend
			} else if pt.TimePoint.Hour() == 12 {
				kind = typeLunch
			} else {
				kind = typeNight
			}
		}

		if kind != currentKind {
			saveCurrentRange()
			currentKind = kind
		}
		if kind != typeNone {
			currentRange = append(currentRange, pt.Index)
		}
	}
	saveCurrentRange()

	drawShading := func(ranges [][]int, colorVal color.RGBA) {
		for _, r := range ranges {
			startIdx := float64(r[0]) - 0.5
			endIdx := float64(r[len(r)-1]) + 0.5

			polyPoints := plotter.XYs{
				{X: startIdx, Y: 0},
				{X: endIdx, Y: 0},
				{X: endIdx, Y: yMax * 1.15},
				{X: startIdx, Y: yMax * 1.15},
			}

			poly, err := plotter.NewPolygon(polyPoints)
			if err == nil {
				poly.Color = colorVal
				poly.LineStyle.Width = 0
				p.Add(poly)
			}
		}
	}

	drawShading(weekendRanges, color.RGBA{R: 241, G: 243, B: 244, A: 255})
	drawShading(lunchRanges, color.RGBA{R: 254, G: 247, B: 224, A: 255})
	drawShading(nightRanges, color.RGBA{R: 238, G: 240, B: 252, A: 255})

	if len(testXYs) > 0 {
		testLine, err := plotter.NewLine(testXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create test line: %w", err)
		}
		testLine.Color = color.RGBA{R: 16, G: 150, B: 24, A: 255}
		testLine.Width = vg.Points(0.8)
		p.Add(testLine)
	}

	if len(anomalyXYs) > 0 {
		anomalyScatter, err := plotter.NewScatter(anomalyXYs)
		if err != nil {
			return nil, fmt.Errorf("failed to create anomaly scatter: %w", err)
		}
		anomalyScatter.Color = color.RGBA{R: 220, G: 57, B: 18, A: 255}
		anomalyScatter.Radius = vg.Points(2)
		anomalyScatter.Shape = draw.CircleGlyph{}
		p.Add(anomalyScatter)
	}

	legendEntries := []legendItem{
		{
			label:  "Testing Logs (Normal/Active)",
			color:  color.RGBA{R: 16, G: 150, B: 24, A: 255},
			isLine: true,
		},
		{
			label:  "Ground Truth Anomalies (Random Spike ONLY)",
			color:  color.RGBA{R: 220, G: 57, B: 18, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Lunchtime (Background Normal)",
			color:  color.RGBA{R: 254, G: 247, B: 224, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Nighttime (Background Normal)",
			color:  color.RGBA{R: 238, G: 240, B: 252, A: 255},
			isLine: false,
		},
		{
			label:  "Silent Weekend (Background Normal)",
			color:  color.RGBA{R: 241, G: 243, B: 244, A: 255},
			isLine: false,
		},
	}
	p.Add(horizontalLegend{entries: legendEntries})
	p.X.Label.Padding = vg.Points(35)

	var ticks []plot.Tick
	trainWeeks := int(trainEndTime.Sub(startTime) / (7 * 24 * time.Hour))
	testWeeks := int(endTime.Sub(trainEndTime) / (7 * 24 * time.Hour))
	for w := trainWeeks; w <= trainWeeks+testWeeks; w++ {
		val := w * slotsPerWeek
		label := fmt.Sprintf("Week %d", w)
		if w == trainWeeks {
			label = "Test Start"
		}
		ticks = append(ticks, plot.Tick{
			Value: float64(val),
			Label: label,
		})
	}
	p.X.Tick.Marker = plot.ConstantTicks(ticks)

	return p, nil
}
