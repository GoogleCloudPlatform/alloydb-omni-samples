import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Scatter,
  ScatterChart,
  Tooltip,
  XAxis,
  YAxis,
  ZAxis,
} from "recharts";
import { metricsApi } from "../api/metrics";
import "./PerformanceDashboard.css";

/**
 * Performance dashboard — visualizes rows from GET /metrics and aggregates from GET /metrics/summary.
 *
 * Data model (each row is one instrumented request):
 * - Timestamps `t` on charts are ISO strings from the backend; X-axis is chronological order in the
 *   series arrays (Recharts does not parse time — spacing is even per sample).
 * - `mcp_routing_ms`: Gemini-based tool router picking nl2sql | semantic_search | monthly_reports | …
 * - `nl2sql_*`: natural-language → SQL translation plus execution; `nl2sql_total_ms` is end-to-end for that tool path.
 * - `semantic_*`: embedding the query + pgvector (or similar) retrieval; `semantic_total_ms` sums the chain.
 * - `gemini_call_ms`: time in Vertex/Gemini for the dominant LLM call on that request (varies by route).
 * - `e2e_total_ms`: wall time for the whole HTTP handler from entry to response.
 * - `nl2sql_pct`, `gemini_pct`, `other_pct`: derived shares of e2e (see backend compute_pct_fields); the pie uses
 *   *averages of those percentages* from the summary endpoint, not a single request’s slice.
 *
 * “Last 50” series: we slice the tail of the full record list so recent behavior is readable; the histogram uses
 * *all* loaded records to approximate the distribution. KPI tiles use summary statistics over the full sample.
 */

const COLORS = {
  teal: "#2dd4bf",
  purple: "#a78bfa",
  amber: "#fbbf24",
  coral: "#fb7185",
  lime: "#a3e635",
};

/**
 * Traffic-light border for numeric KPIs: `greenMax` and `yellowMax` are upper bounds in ms (or % for truncation).
 * null/NaN → neutral styling.
 */
function latencyClass(ms, greenMax, yellowMax) {
  if (ms == null || Number.isNaN(ms)) return "neutral";
  if (ms < greenMax) return "good";
  if (ms < yellowMax) return "warn";
  return "bad";
}

/** NL2SQL truncation: high rate means many queries hit row limits — warn/red thresholds in percent. */
function truncRateClass(rate) {
  if (rate == null) return "neutral";
  if (rate < 5) return "good";
  if (rate < 25) return "warn";
  return "bad";
}

function fmtMs(v) {
  if (v == null || Number.isNaN(v)) return "—";
  return `${Math.round(v)} ms`;
}

function fmtNum(v) {
  if (v == null || Number.isNaN(v)) return "—";
  return Number(v).toLocaleString(undefined, { maximumFractionDigits: 1 });
}

/**
 * Build a fixed-bin histogram of end-to-end latency for the bar chart (“E2E latency distribution”).
 *
 * Recharts has no first-class histogram, so we pre-bucket:
 * 1. Collect every `e2e_total_ms` from the full `records` array (not just last 50) so the distribution
 *    reflects the entire persisted window (up to backend MAX_RECORDS).
 * 2. Find min/max of those values; if all identical, `span` becomes 1 to avoid divide-by-zero.
 * 3. Split [min, max] into `bins` equal-width intervals; label each bar with the ms range endpoints.
 * 4. For each sample, compute `floor((v - min) / width)` and increment that bucket; clamp index into [0, bins-1]
 *    so the maximum value lands in the last bin (inclusive upper edge).
 *
 * The Y-axis is a count of requests; shape shows clustering vs long-tail outliers for overall handler latency.
 */
function bucketE2e(records, bins = 12) {
  const vals = records.map((r) => r.e2e_total_ms).filter((v) => v != null && !Number.isNaN(v));
  if (!vals.length) return [];
  const min = Math.min(...vals);
  const max = Math.max(...vals);
  const span = max - min || 1;
  const width = span / bins;
  const rows = Array.from({ length: bins }, (_, i) => ({
    name: `${Math.round(min + i * width)}–${Math.round(min + (i + 1) * width)}`,
    count: 0,
    mid: min + (i + 0.5) * width,
  }));
  for (const v of vals) {
    let idx = Math.floor((v - min) / width);
    if (idx >= bins) idx = bins - 1;
    if (idx < 0) idx = 0;
    rows[idx].count += 1;
  }
  return rows;
}

export default function PerformanceDashboard() {
  const [records, setRecords] = useState([]);
  const [summary, setSummary] = useState(null);
  const [error, setError] = useState("");
  const [lastFetchAt, setLastFetchAt] = useState(null);
  const [tick, setTick] = useState(0);

  const load = useCallback(async () => {
    try {
      const [recs, sum] = await Promise.all([metricsApi.fetchMetrics(), metricsApi.fetchSummary()]);
      setRecords(Array.isArray(recs) ? recs : []);
      setSummary(sum);
      setError("");
      setLastFetchAt(Date.now());
    } catch (e) {
      setError(e.message || String(e));
    }
  }, []);

  // Poll both raw rows and server-computed summary so charts and KPIs stay in sync with the shared store/CSV.
  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  useEffect(() => {
    const id = setInterval(() => setTick((x) => x + 1), 1000);
    return () => clearInterval(id);
  }, []);

  const secondsAgo = useMemo(() => {
    if (!lastFetchAt) return null;
    void tick;
    return Math.floor((Date.now() - lastFetchAt) / 1000);
  }, [lastFetchAt, tick]);

  const handleClear = async () => {
    try {
      await metricsApi.clearMetrics();
      await load();
    } catch (e) {
      setError(e.message || String(e));
    }
  };

  /** Most charts only need recent points; 50 keeps the line charts readable without overcrowding. */
  const last50 = useMemo(() => records.slice(-50), [records]);

  /**
   * Multi-line chart: per-sample latency components sharing one time-ordered X.
   * Each line may be null for a given request (tool did not run); gaps appear where data is missing.
   * Y is milliseconds. Series are not stacked — they overlap so you can compare which subsystem dominates each event.
   */
  const seriesLatency = useMemo(() => {
    return last50.map((r) => ({
      t: r.timestamp,
      mcp_routing_ms: r.mcp_routing_ms ?? null,
      nl2sql_total_ms: r.nl2sql_total_ms ?? null,
      semantic_total_ms: r.semantic_total_ms ?? null,
      gemini_call_ms: r.gemini_call_ms ?? null,
      e2e_total_ms: r.e2e_total_ms ?? null,
    }));
  }, [last50]);

  /**
   * Stacked area chart for NL2SQL-only samples: `translation` = LLM NL→SQL timing; `execution` = DB round-trip.
   * Both stackId="a" so Recharts stacks them; total height ≈ nl2sql_translation_ms + nl2sql_execution_ms for that row.
   * Requests without either field are omitted so the chart is not diluted by non-NL2SQL traffic.
   */
  const seriesNl2sqlSteps = useMemo(() => {
    return last50
      .filter((r) => r.nl2sql_translation_ms != null || r.nl2sql_execution_ms != null)
      .map((r) => ({
        t: r.timestamp,
        translation: r.nl2sql_translation_ms ?? 0,
        execution: r.nl2sql_execution_ms ?? 0,
      }));
  }, [last50]);

  /**
   * Stacked area for semantic-search path: `embedding` vs `semantic_vector_search_ms`.
   * Shows whether query embedding or vector DB retrieval is the bottleneck over recent semantic requests.
   */
  const seriesSemanticSteps = useMemo(() => {
    return last50
      .filter((r) => r.semantic_embedding_ms != null || r.semantic_vector_search_ms != null)
      .map((r) => ({
        t: r.timestamp,
        embedding: r.semantic_embedding_ms ?? 0,
        search: r.semantic_vector_search_ms ?? 0,
      }));
  }, [last50]);

  const histData = useMemo(() => bucketE2e(records, 12), [records]);

  /**
   * Scatter: each point is one NL2SQL request with both row count and total NL2SQL latency.
   * X = rows returned (or scanned — backend-defined); Y = ms. Use this to see correlation between result size and time.
   */
  const scatterNl2sql = useMemo(() => {
    return records
      .filter((r) => r.nl2sql_row_count != null && r.nl2sql_total_ms != null)
      .map((r) => ({
        x: r.nl2sql_row_count,
        y: r.nl2sql_total_ms,
        id: r.id,
      }));
  }, [records]);

  /**
   * Pie chart uses /metrics/summary averages of nl2sql_pct, gemini_pct, other_pct — **not** summing raw ms.
   * Those percentages are per-request shares of e2e_total_ms (NL2SQL total, Gemini call, remainder); averaging them
   * approximates “typical composition” across the sample. If no summary fields, shows a placeholder slice.
   */
  const pieBreakdown = useMemo(() => {
    const s = summary || {};
    const n = s.nl2sql_pct?.avg;
    const g = s.gemini_pct?.avg;
    const o = s.other_pct?.avg;
    const parts = [];
    if (n != null) parts.push({ name: "NL2SQL %", value: Math.max(0, n), fill: COLORS.teal });
    if (g != null) parts.push({ name: "Gemini %", value: Math.max(0, g), fill: COLORS.purple });
    if (o != null) parts.push({ name: "Other %", value: Math.max(0, o), fill: COLORS.amber });
    return parts.length ? parts : [{ name: "No data", value: 1, fill: "#334155" }];
  }, [summary]);

  /** Payload size of Gemini/Vertex responses over time (when recorded); correlates with prompt+output token volume. */
  const lineGeminiBytes = useMemo(() => {
    return last50
      .filter((r) => r.gemini_response_bytes != null)
      .map((r) => ({ t: r.timestamp, bytes: r.gemini_response_bytes }));
  }, [last50]);

  /** How many transactions matched the semantic filter per request — throughput / relevance of retrieval. */
  const lineSemanticMatches = useMemo(() => {
    return last50
      .filter((r) => r.semantic_match_count != null)
      .map((r) => ({ t: r.timestamp, matches: r.semantic_match_count }));
  }, [last50]);

  /** Debug table: newest events first, capped at 20 rows; same underlying fields as CSV/API. */
  const tableRows = useMemo(() => {
    return [...records].slice(-20).reverse();
  }, [records]);

  const st = (key) => summary?.[key];

  const kpiRouting = st("mcp_routing_ms");
  const kpiNl2sql = st("nl2sql_total_ms");
  const kpiSemantic = st("semantic_total_ms");
  const kpiGemini = st("gemini_call_ms");
  const kpiE2e = st("e2e_total_ms");
  const truncRate = summary?.nl2sql_truncation_rate_pct;

  const HIGH_E2E = 4000;
  const HIGH_NL2SQL = 3000;

  return (
    <div className="perf-root">
      <div className="perf-header">
        <div>
          <p style={{ margin: 0, fontSize: "0.9rem", color: "#94a3b8" }}>
            Live metrics from in-memory store (last 500 requests)
          </p>
        </div>
        <div className="perf-actions">
          {secondsAgo != null && (
            <span className="perf-updated">Last updated: {secondsAgo}s ago</span>
          )}
          <button type="button" className="perf-btn-clear" onClick={handleClear}>
            Clear metrics
          </button>
        </div>
      </div>

      {error && <div className="perf-error">{error}</div>}

      <p className="perf-panel-desc" style={{ marginBottom: "0.75rem" }}>
        Roll-up stats across the full sample in the store (with p95 where shown); not limited to the last 50 chart points.
      </p>

      <div className="perf-kpi-row">
        <div className={`perf-kpi ${latencyClass(kpiRouting?.avg, 150, 600)}`}>
          <div className="perf-kpi-title">Avg MCP routing</div>
          <div className="perf-kpi-avg">{fmtMs(kpiRouting?.avg)}</div>
          <div className="perf-kpi-sub">p95 {fmtMs(kpiRouting?.p95)}</div>
        </div>
        <div className={`perf-kpi ${latencyClass(kpiNl2sql?.avg, 800, 2500)}`}>
          <div className="perf-kpi-title">Avg NL2SQL total</div>
          <div className="perf-kpi-avg">{fmtMs(kpiNl2sql?.avg)}</div>
          <div className="perf-kpi-sub">p95 {fmtMs(kpiNl2sql?.p95)}</div>
        </div>
        <div className={`perf-kpi ${latencyClass(kpiSemantic?.avg, 400, 1200)}`}>
          <div className="perf-kpi-title">Avg semantic search</div>
          <div className="perf-kpi-avg">{fmtMs(kpiSemantic?.avg)}</div>
          <div className="perf-kpi-sub">p95 {fmtMs(kpiSemantic?.p95)}</div>
        </div>
        <div className={`perf-kpi ${latencyClass(kpiGemini?.avg, 800, 2000)}`}>
          <div className="perf-kpi-title">Avg Gemini call</div>
          <div className="perf-kpi-avg">{fmtMs(kpiGemini?.avg)}</div>
          <div className="perf-kpi-sub">p95 {fmtMs(kpiGemini?.p95)}</div>
        </div>
        <div className={`perf-kpi ${latencyClass(kpiE2e?.avg, 1500, 4000)}`}>
          <div className="perf-kpi-title">Avg E2E</div>
          <div className="perf-kpi-avg">{fmtMs(kpiE2e?.avg)}</div>
          <div className="perf-kpi-sub">p95 {fmtMs(kpiE2e?.p95)}</div>
        </div>
        <div className={`perf-kpi ${truncRateClass(truncRate)}`}>
          <div className="perf-kpi-title">NL2SQL truncation rate</div>
          <div className="perf-kpi-avg">
            {truncRate != null ? `${fmtNum(truncRate)}%` : "—"}
          </div>
          <div className="perf-kpi-sub">sample {summary?.sample_size ?? 0}</div>
        </div>
      </div>

      <div className="perf-grid-2">
        <div className="perf-panel">
          <div className="perf-panel-title">Latency over time (last 50)</div>
          <p className="perf-panel-desc">
            Tracks major latency layers across recent instrumented requests so you can spot spikes and which subsystem drove them.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              {/* XAxis kept as index via dataKey="t" but ticks hidden — ISO timestamps are wide; order still follows array sequence. */}
              <LineChart data={seriesLatency} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis dataKey="t" tick={{ fill: "#64748b", fontSize: 10 }} hide />
                <YAxis tick={{ fill: "#64748b", fontSize: 10 }} />
                <Tooltip
                  contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }}
                  labelStyle={{ color: "#94a3b8" }}
                />
                <Legend />
                {/* monotone = curved segments between samples; dots off for noisy series; colors distinguish overlapping lines */}
                <Line type="monotone" dataKey="mcp_routing_ms" name="MCP routing" stroke={COLORS.teal} dot={false} isAnimationActive animationDuration={400} />
                <Line type="monotone" dataKey="nl2sql_total_ms" name="NL2SQL total" stroke={COLORS.purple} dot={false} isAnimationActive animationDuration={400} />
                <Line type="monotone" dataKey="semantic_total_ms" name="Semantic total" stroke={COLORS.amber} dot={false} isAnimationActive animationDuration={400} />
                <Line type="monotone" dataKey="gemini_call_ms" name="Gemini" stroke={COLORS.coral} dot={false} isAnimationActive animationDuration={400} />
                <Line type="monotone" dataKey="e2e_total_ms" name="E2E" stroke={COLORS.lime} dot={false} isAnimationActive animationDuration={400} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        </div>

        <div className="perf-panel">
          <div className="perf-panel-title">NL2SQL sub-steps (stacked)</div>
          <p className="perf-panel-desc">
            Stacked translation vs execution time on NL2SQL requests only, showing whether SQL generation or the database dominates.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={seriesNl2sqlSteps} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis dataKey="t" tick={{ fill: "#64748b", fontSize: 10 }} hide />
                <YAxis tick={{ fill: "#64748b", fontSize: 10 }} />
                <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Legend />
                {/* stackId groups areas into one stacked column per index; bottom = translation, top adds execution */}
                <Area type="monotone" dataKey="translation" name="Translation" stackId="a" stroke={COLORS.teal} fill={COLORS.teal} isAnimationActive animationDuration={400} />
                <Area type="monotone" dataKey="execution" name="Execution" stackId="a" stroke={COLORS.purple} fill={COLORS.purple} isAnimationActive animationDuration={400} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      <div className="perf-grid-2">
        <div className="perf-panel">
          <div className="perf-panel-title">Semantic sub-steps (stacked)</div>
          <p className="perf-panel-desc">
            Splits semantic search time into embedding vs vector retrieval for recent requests that used that path.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={seriesSemanticSteps} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis dataKey="t" tick={{ fill: "#64748b", fontSize: 10 }} hide />
                <YAxis tick={{ fill: "#64748b", fontSize: 10 }} />
                <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Legend />
                {/* stackId "s" separate from NL2SQL "a" so the two charts never share stacking state */}
                <Area type="monotone" dataKey="embedding" name="Embedding" stackId="s" stroke={COLORS.teal} fill={COLORS.teal} isAnimationActive animationDuration={400} />
                <Area type="monotone" dataKey="search" name="Vector search" stackId="s" stroke={COLORS.amber} fill={COLORS.amber} isAnimationActive animationDuration={400} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>

        <div className="perf-panel">
          <div className="perf-panel-title">E2E latency distribution</div>
          <p className="perf-panel-desc">
            Histogram of end-to-end handler times over all stored samples, revealing typical latency and how heavy the tail is.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={histData} margin={{ top: 8, right: 8, left: 0, bottom: 24 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis dataKey="name" tick={{ fill: "#64748b", fontSize: 9 }} interval={0} angle={-35} textAnchor="end" height={60} />
                <YAxis tick={{ fill: "#64748b", fontSize: 10 }} allowDecimals={false} />
                <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Bar dataKey="count" name="Requests" fill={COLORS.teal} radius={[4, 4, 0, 0]} isAnimationActive animationDuration={400} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      <div className="perf-grid-2">
        <div className="perf-panel">
          <div className="perf-panel-title">Row count vs NL2SQL latency</div>
          <p className="perf-panel-desc">
            Each point is one NL2SQL call; compares rows returned (or processed) to total NL2SQL duration to spot size-driven slowdowns.
          </p>
          <div className="perf-chart-wrap tall">
            <ResponsiveContainer width="100%" height="100%">
              <ScatterChart margin={{ top: 8, right: 8, bottom: 8, left: 8 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis type="number" dataKey="x" name="rows" tick={{ fill: "#64748b", fontSize: 10 }} />
                <YAxis type="number" dataKey="y" name="ms" tick={{ fill: "#64748b", fontSize: 10 }} />
                {/* ZAxis fixed range → uniform dot diameter (third dimension unused but required by ScatterChart). */}
                <ZAxis range={[60, 60]} />
                <Tooltip cursor={{ strokeDasharray: "3 3" }} contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Scatter name="NL2SQL" data={scatterNl2sql} fill={COLORS.purple} isAnimationActive animationDuration={400} />
              </ScatterChart>
            </ResponsiveContainer>
          </div>
        </div>

        <div className="perf-panel">
          <div className="perf-panel-title">E2E breakdown (avg %)</div>
          <p className="perf-panel-desc">
            Average share of wall time attributed to NL2SQL, Gemini, and everything else, computed from per-request percentage fields in the summary API.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              <PieChart>
                {/* `label` renders slice names on chart; values are already 0–100 style percentages from summary.avg */}
                <Pie data={pieBreakdown} dataKey="value" nameKey="name" cx="50%" cy="50%" outerRadius={88} label isAnimationActive animationDuration={400}>
                  {pieBreakdown.map((entry) => (
                    <Cell key={entry.name} fill={entry.fill} />
                  ))}
                </Pie>
                <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Legend />
              </PieChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      <div className="perf-grid-2">
        <div className="perf-panel">
          <div className="perf-panel-title">Gemini response bytes (last 50 w/ data)</div>
          <p className="perf-panel-desc">
            Size of the raw Vertex HTTP response body (bytes) when a monthly-report Gemini call completes; other tools do not write this field yet.
          </p>
          {lineGeminiBytes.length === 0 ? (
            <p className="perf-chart-empty">No samples yet — generate a monthly report (or use Insights with a monthly question) so the LLM run records response size.</p>
          ) : (
            <div className="perf-chart-wrap">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={lineGeminiBytes} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                  <XAxis dataKey="t" tick={{ fill: "#64748b", fontSize: 10 }} hide />
                  <YAxis tick={{ fill: "#64748b", fontSize: 10 }} />
                  <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                  <Line type="monotone" dataKey="bytes" name="bytes" stroke={COLORS.coral} dot={false} isAnimationActive animationDuration={400} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>

        <div className="perf-panel">
          <div className="perf-panel-title">Semantic match count (last 50)</div>
          <p className="perf-panel-desc">
            Number of matching transactions returned per semantic search call over recent history.
          </p>
          <div className="perf-chart-wrap">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={lineSemanticMatches} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2a2f3d" />
                <XAxis dataKey="t" tick={{ fill: "#64748b", fontSize: 10 }} hide />
                <YAxis tick={{ fill: "#64748b", fontSize: 10 }} allowDecimals={false} />
                <Tooltip contentStyle={{ background: "#1a1d27", border: "1px solid #2a2f3d" }} />
                <Line type="monotone" dataKey="matches" name="matches" stroke={COLORS.teal} dot={false} isAnimationActive animationDuration={400} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>

      <div className="perf-panel" style={{ marginBottom: "1rem" }}>
        <div className="perf-panel-title">Recent records (newest first, max 20)</div>
        <p className="perf-panel-desc">
          Raw per-request metrics for the latest events, mirroring the API/CSV for quick debugging.
        </p>
        <div className="perf-table-wrap">
          <table className="perf-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>Tool</th>
                <th>E2E</th>
                <th>MCP</th>
                <th>NL2SQL Σ</th>
                <th>Semantic Σ</th>
                <th>Gemini</th>
                <th>Rows</th>
                <th>Trunc</th>
              </tr>
            </thead>
            <tbody>
              {tableRows.map((r) => (
                <tr key={r.id}>
                  <td>{r.timestamp}</td>
                  <td>{r.tool}</td>
                  <td className={r.e2e_total_ms > HIGH_E2E ? "perf-cell-high" : ""}>{fmtMs(r.e2e_total_ms)}</td>
                  <td>{fmtMs(r.mcp_routing_ms)}</td>
                  <td className={r.nl2sql_total_ms > HIGH_NL2SQL ? "perf-cell-high" : ""}>{fmtMs(r.nl2sql_total_ms)}</td>
                  <td>{fmtMs(r.semantic_total_ms)}</td>
                  <td>{fmtMs(r.gemini_call_ms)}</td>
                  <td>{r.nl2sql_row_count ?? "—"}</td>
                  <td>{r.nl2sql_truncated === true ? "yes" : r.nl2sql_truncated === false ? "no" : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
