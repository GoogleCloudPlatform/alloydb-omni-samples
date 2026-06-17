import { useState, useCallback, useMemo } from "react";
import { clustersApi } from "../api/clusters";
import TransactionTable from "./TransactionTable";

const TIME_CHIPS = [
  { label: "Last 7 days",   value: "7d"     },
  { label: "Last 30 days",  value: "30d"    },
  { label: "Last 3 months", value: "90d"    },
  { label: "Year to date",  value: "ytd"    },
  { label: "All time",      value: "all"    },
  { label: "Custom",        value: "custom" },
];

const CLUSTER_COLORS = [
  { bg: "#dbeafe", border: "#93c5fd", text: "#1e40af" },
  { bg: "#dcfce7", border: "#86efac", text: "#166534" },
  { bg: "#fef3c7", border: "#fcd34d", text: "#92400e" },
  { bg: "#ede9fe", border: "#c4b5fd", text: "#5b21b6" },
  { bg: "#fce7f3", border: "#f9a8d4", text: "#9d174d" },
  { bg: "#e0f2fe", border: "#7dd3fc", text: "#075985" },
  { bg: "#ccfbf1", border: "#5eead4", text: "#115e59" },
  { bg: "#fef9c3", border: "#fde047", text: "#854d0e" },
  { bg: "#fee2e2", border: "#fca5a5", text: "#991b1b" },
  { bg: "#e0e7ff", border: "#a5b4fc", text: "#3730a3" },
];

function Sparkline({ data, color }) {
  if (!data || data.length < 2) return null;
  const max = Math.max(...data.map((d) => d.spend), 1);
  const W = 80, H = 24, gap = 1;
  const barW = Math.max(2, Math.floor((W - gap * (data.length - 1)) / data.length));
  return (
    <svg width={W} height={H} style={{ verticalAlign: "middle", color }}>
      {data.map((d, i) => {
        const h = Math.max(2, Math.round((d.spend / max) * H));
        return (
          <rect
            key={i}
            x={i * (barW + gap)}
            y={H - h}
            width={barW}
            height={h}
            fill="currentColor"
            opacity={0.55}
            rx={1}
          />
        );
      })}
    </svg>
  );
}

function TrendBadge({ trend }) {
  if (!trend || trend.direction === "flat") return null;
  if (trend.direction === "new") return <span className="trend-badge trend-new">New</span>;
  const arrow = trend.direction === "up" ? "↑" : "↓";
  const cls = trend.direction === "up" ? "trend-badge trend-up" : "trend-badge trend-down";
  return (
    <span className={cls}>
      {arrow} {Math.abs(trend.percent_change)}% vs prior period
    </span>
  );
}

export default function SemanticClusters() {
  const [k, setK] = useState(5);
  const [timeRange, setTimeRange] = useState("30d");
  const [startDate, setStartDate] = useState("");
  const [endDate, setEndDate] = useState("");
  const [compareMode, setCompareMode] = useState(false);
  const [clusters, setClusters] = useState([]);
  const [previousClusters, setPreviousClusters] = useState(null);
  const [clusterMeta, setClusterMeta] = useState(null);
  const [summary, setSummary] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [sortBy, setSortBy] = useState("total_spend");
  const [spendThreshold, setSpendThreshold] = useState(0);
  const [expandedCluster, setExpandedCluster] = useState(null);
  const [clusterPages, setClusterPages] = useState({});

  const handleCluster = useCallback(async () => {
    setLoading(true);
    setError("");
    setExpandedCluster(null);
    setClusterPages({});
    try {
      const timeOpts =
        timeRange === "custom"
          ? { startDate, endDate }
          : { timeRange };
      const data = await clustersApi.getClusters({ k, compare: compareMode, ...timeOpts });
      setClusters(data.clusters);
      setPreviousClusters(data.previous_clusters || null);
      setSummary(data.summary || []);
      setClusterMeta({
        k: data.k,
        time_range: data.time_range,
        total: data.total_transactions,
        embedded: data.transactions_with_embeddings,
      });
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, [k, timeRange, startDate, endDate, compareMode]);

  const displayedClusters = useMemo(() => {
    let out = clusters.filter((c) => c.total_spend >= spendThreshold);
    if (sortBy === "transaction_count") out.sort((a, b) => b.transaction_count - a.transaction_count);
    else if (sortBy === "avg_similarity") out.sort((a, b) => b.avg_similarity - a.avg_similarity);
    else out.sort((a, b) => b.total_spend - a.total_spend);
    return out;
  }, [clusters, sortBy, spendThreshold]);

  const disappearedClusters = useMemo(() => {
    if (!previousClusters) return [];
    const currentCats = new Set(clusters.map((c) => c.majority_category));
    return previousClusters.filter((c) => !currentCats.has(c.majority_category));
  }, [clusters, previousClusters]);

  return (
    <div className="semantic-clusters-panel">
      {/* Controls card */}
      <section className="card">
        <div className="table-header">
          <h2>Semantic Clusters</h2>
          {clusterMeta && (
            <span className="txn-count">
              {clusterMeta.embedded} of {clusterMeta.total} transactions · {clusterMeta.time_range}
            </span>
          )}
        </div>

        {/* Time chips */}
        <div className="filter-group" style={{ marginBottom: "0.75rem" }}>
          {TIME_CHIPS.map((chip) => (
            <button
              key={chip.value}
              className={`btn btn-filter ${timeRange === chip.value ? "active" : ""}`}
              onClick={() => setTimeRange(chip.value)}
            >
              {chip.label}
            </button>
          ))}
        </div>

        {timeRange === "custom" && (
          <div className="semantic-filter-row" style={{ marginBottom: "0.75rem" }}>
            <input
              type="date"
              className="category-select"
              value={startDate}
              onChange={(e) => setStartDate(e.target.value)}
            />
            <span style={{ color: "#64748b", fontSize: "0.85rem" }}>to</span>
            <input
              type="date"
              className="category-select"
              value={endDate}
              onChange={(e) => setEndDate(e.target.value)}
            />
          </div>
        )}

        {/* Controls row */}
        <div className="filter-group" style={{ alignItems: "center", flexWrap: "wrap", gap: "1rem" }}>
          <label className="cluster-slider-label">
            <span>Clusters: <strong>{k}</strong></span>
            <input
              type="range"
              min={3}
              max={10}
              value={k}
              onChange={(e) => setK(Number(e.target.value))}
              className="cluster-slider"
            />
          </label>

          <label style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.85rem", fontWeight: 500, color: "#475569", cursor: "pointer" }}>
            <input
              type="checkbox"
              checked={compareMode}
              onChange={(e) => setCompareMode(e.target.checked)}
              style={{ accentColor: "#2563eb" }}
            />
            Compare periods
          </label>

          <select
            className="category-select"
            value={sortBy}
            onChange={(e) => setSortBy(e.target.value)}
          >
            <option value="total_spend">Sort: Total Spend</option>
            <option value="transaction_count">Sort: Transaction Count</option>
            <option value="avg_similarity">Sort: Avg Similarity</option>
          </select>

          {spendThreshold > 0 && (
            <label className="cluster-slider-label" style={{ minWidth: 200 }}>
              <span>Min spend: <strong>${spendThreshold.toLocaleString()}</strong></span>
              <input
                type="range"
                min={0}
                max={2000}
                step={50}
                value={spendThreshold}
                onChange={(e) => setSpendThreshold(Number(e.target.value))}
                className="cluster-slider"
              />
            </label>
          )}
          {spendThreshold === 0 && (
            <button
              className="btn btn-filter"
              onClick={() => setSpendThreshold(50)}
              style={{ fontSize: "0.8rem" }}
            >
              + Min spend filter
            </button>
          )}
          {spendThreshold > 0 && (
            <button
              className="btn btn-filter"
              onClick={() => setSpendThreshold(0)}
              style={{ fontSize: "0.8rem" }}
            >
              Clear spend filter
            </button>
          )}

          <button className="btn btn-primary" onClick={handleCluster} disabled={loading}>
            {loading ? "Clustering…" : "Cluster →"}
          </button>
        </div>

        {error && <p className="global-error" style={{ marginTop: "0.75rem" }}>{error}</p>}
      </section>

      {/* Spending summary */}
      {!loading && summary.length > 0 && (
        <section className="card cluster-summary-card">
          <h3 className="cluster-summary-title">Spending snapshot</h3>
          <ul className="cluster-summary-list">
            {summary.map((bullet, i) => (
              <li key={i} className="cluster-summary-item">
                <span
                  className="cluster-summary-dot"
                  style={{ background: CLUSTER_COLORS[displayedClusters[i]?.cluster_id % CLUSTER_COLORS.length]?.border || "#94a3b8" }}
                />
                {bullet}
              </li>
            ))}
          </ul>
        </section>
      )}

      {/* Cluster cards */}
      {!loading && displayedClusters.length > 0 && (
        <div className="cluster-grid">
          {displayedClusters.map((cluster) => {
            const color = CLUSTER_COLORS[cluster.cluster_id % CLUSTER_COLORS.length];
            const isExpanded = expandedCluster === cluster.cluster_id;
            return (
              <div
                key={cluster.cluster_id}
                className="cluster-card"
                style={{ borderColor: color.border, background: color.bg }}
              >
                <div className="cluster-card-header">
                  <div style={{ flex: 1 }}>
                    <span className="cluster-label">{cluster.label}</span>
                    <div className="cluster-stats">
                      <span className="cluster-stat">{cluster.transaction_count} txns</span>
                      <span className="cluster-stat">·</span>
                      <span className="cluster-stat">
                        ${cluster.total_spend.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
                      </span>
                      <span
                        className="similarity-badge"
                        style={{ background: color.border, color: color.text }}
                      >
                        {(cluster.avg_similarity * 100).toFixed(0)}% similar
                      </span>
                      {cluster.trend && <TrendBadge trend={cluster.trend} />}
                      <Sparkline data={cluster.monthly_spend} color={color.text} />
                    </div>
                    {cluster.top_terms.length > 0 && (
                      <div className="cluster-terms">
                        {cluster.top_terms.map((term) => (
                          <span key={term} className="term-chip">{term}</span>
                        ))}
                      </div>
                    )}
                  </div>
                  <button
                    className="btn btn-filter"
                    onClick={() => setExpandedCluster(isExpanded ? null : cluster.cluster_id)}
                    style={{ flexShrink: 0 }}
                  >
                    {isExpanded ? "Collapse" : "Expand"}
                  </button>
                </div>

                {isExpanded && (() => {
                  const CLUSTER_PAGE_SIZE = 10;
                  const page = clusterPages[cluster.cluster_id] || 0;
                  const totalPages = Math.ceil(cluster.transactions.length / CLUSTER_PAGE_SIZE);
                  const pageSlice = cluster.transactions.slice(page * CLUSTER_PAGE_SIZE, (page + 1) * CLUSTER_PAGE_SIZE);
                  const setPage = (p) => setClusterPages((prev) => ({ ...prev, [cluster.cluster_id]: p }));
                  return (
                    <div className="cluster-transactions">
                      {totalPages > 1 && (
                        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "0.5rem" }}>
                          <span style={{ fontSize: "0.82rem", color: "#64748b" }}>
                            Showing {page * CLUSTER_PAGE_SIZE + 1}–{Math.min((page + 1) * CLUSTER_PAGE_SIZE, cluster.transactions.length)} of {cluster.transactions.length}
                          </span>
                          <div className="pagination-row" style={{ margin: 0 }}>
                            <button
                              className="btn btn-filter"
                              onClick={() => setPage(page - 1)}
                              disabled={page === 0}
                            >
                              ← Prev
                            </button>
                            <span className="pagination-info">{page + 1} / {totalPages}</span>
                            <button
                              className="btn btn-filter"
                              onClick={() => setPage(page + 1)}
                              disabled={page >= totalPages - 1}
                            >
                              Next →
                            </button>
                          </div>
                        </div>
                      )}
                      <TransactionTable transactions={pageSlice} loading={false} />
                      {totalPages > 1 && (
                        <div className="pagination-row" style={{ marginTop: "0.5rem" }}>
                          <button
                            className="btn btn-filter"
                            onClick={() => setPage(page - 1)}
                            disabled={page === 0}
                          >
                            ← Prev
                          </button>
                          <span className="pagination-info">{page + 1} / {totalPages}</span>
                          <button
                            className="btn btn-filter"
                            onClick={() => setPage(page + 1)}
                            disabled={page >= totalPages - 1}
                          >
                            Next →
                          </button>
                        </div>
                      )}
                    </div>
                  );
                })()}
              </div>
            );
          })}
        </div>
      )}

      {!loading && clusters.length > 0 && displayedClusters.length === 0 && (
        <p className="table-status">No clusters match the current spend threshold.</p>
      )}

      {!loading && clusters.length === 0 && !error && (
        <p className="table-status">
          Select a time range and click <strong>Cluster →</strong> to group your transactions by spending patterns.
        </p>
      )}

      {/* Compare mode: disappeared clusters */}
      {compareMode && disappearedClusters.length > 0 && (
        <section className="card">
          <div className="table-header">
            <h2>Clusters from prior period (no longer present)</h2>
          </div>
          <div className="cluster-grid">
            {disappearedClusters.map((cluster, i) => (
              <div
                key={cluster.cluster_id}
                className="cluster-card"
                style={{ borderColor: "#e2e8f0", background: "#f8fafc", opacity: 0.7 }}
              >
                <div className="cluster-card-header">
                  <div>
                    <span className="cluster-label" style={{ color: "#94a3b8" }}>{cluster.label}</span>
                    <div className="cluster-stats">
                      <span className="cluster-stat">{cluster.transaction_count} txns</span>
                      <span className="cluster-stat">·</span>
                      <span className="cluster-stat">
                        ${cluster.total_spend.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
                      </span>
                      <span className="trend-badge trend-down">Gone</span>
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
