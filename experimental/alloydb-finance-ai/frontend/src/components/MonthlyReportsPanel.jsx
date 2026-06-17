import { useEffect, useMemo, useState } from "react";
import { jsPDF } from "jspdf";
import { monthlyReportsApi } from "../api/monthlyReports";

function currentMonthValue() {
  const now = new Date();
  const mm = String(now.getMonth() + 1).padStart(2, "0");
  return `${now.getFullYear()}-${mm}`;
}

function formatMoney(value) {
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(value || 0);
}

function formatMonth(value) {
  if (!value || typeof value !== "string" || !value.includes("-")) return "Unknown month";
  const [y, m] = value.split("-");
  return new Date(Number(y), Number(m) - 1, 1).toLocaleDateString(undefined, {
    month: "long",
    year: "numeric",
  });
}

function formatGeneratedAt(value) {
  if (!value) return "Unknown";
  return new Date(value).toLocaleString();
}

function asArray(value) {
  if (Array.isArray(value)) return value;
  if (typeof value === "string") {
    try {
      const parsed = JSON.parse(value);
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  }
  return [];
}

function normalizeSuggestion(item) {
  if (item && typeof item === "object" && item.title) return item;
  const text = typeof item === "string" ? item : String(item ?? "");
  return { title: text, rationale: "", estimated_savings_usd: 0, difficulty: "", impact: "" };
}

function normalizeReport(raw) {
  const yearMonth = typeof raw?.year_month === "string" ? raw.year_month : "";
  return {
    year_month: yearMonth,
    total_spent: Number(raw?.total_spent || 0),
    comments: typeof raw?.comments === "string" ? raw.comments : "",
    headline: typeof raw?.headline === "string" ? raw.headline : "",
    suggestions: asArray(raw?.suggestions).map(normalizeSuggestion).filter((s) => s.title),
    watch_items: asArray(raw?.watch_items).filter(Boolean),
    category_breakdown: asArray(raw?.category_breakdown),
    merchant_breakdown: asArray(raw?.merchant_breakdown),
    generated_at: raw?.generated_at || null,
  };
}

const DIFFICULTY_COLOR = { Easy: "#16a34a", Medium: "#d97706", Hard: "#dc2626" };
const IMPACT_BG = { Low: "#f1f5f9", Medium: "#fef3c7", High: "#dcfce7" };
const IMPACT_TEXT = { Low: "#475569", Medium: "#92400e", High: "#166534" };

function DifficultyBadge({ difficulty }) {
  if (!difficulty) return null;
  return (
    <span style={{
      fontSize: "0.7rem",
      fontWeight: 600,
      padding: "0.15rem 0.45rem",
      borderRadius: "99px",
      border: `1px solid ${DIFFICULTY_COLOR[difficulty] || "#94a3b8"}`,
      color: DIFFICULTY_COLOR[difficulty] || "#94a3b8",
      letterSpacing: "0.03em",
    }}>
      {difficulty}
    </span>
  );
}

function ImpactBadge({ impact }) {
  if (!impact) return null;
  return (
    <span style={{
      fontSize: "0.7rem",
      fontWeight: 600,
      padding: "0.15rem 0.45rem",
      borderRadius: "99px",
      background: IMPACT_BG[impact] || "#f1f5f9",
      color: IMPACT_TEXT[impact] || "#475569",
    }}>
      {impact} impact
    </span>
  );
}

function CategoryBar({ category, totalSpent, maxSpent, index }) {
  const pct = maxSpent > 0 ? Math.max((totalSpent / maxSpent) * 100, 3) : 0;
  const COLORS = ["#6366f1", "#8b5cf6", "#a78bfa", "#c4b5fd", "#ddd6fe", "#ede9fe", "#f5f3ff", "#fafafa"];
  return (
    <div style={{ display: "flex", alignItems: "center", gap: "0.6rem", marginBottom: "0.45rem" }}>
      <span style={{ width: "100px", fontSize: "0.8rem", color: "#475569", flexShrink: 0, textOverflow: "ellipsis", overflow: "hidden", whiteSpace: "nowrap" }}>
        {category}
      </span>
      <div style={{ flex: 1, background: "#f1f5f9", borderRadius: "99px", height: "8px", overflow: "hidden" }}>
        <div style={{ width: `${pct}%`, height: "100%", background: COLORS[index % COLORS.length], borderRadius: "99px", transition: "width 0.4s ease" }} />
      </div>
      <span style={{ width: "72px", fontSize: "0.8rem", color: "#334155", textAlign: "right", flexShrink: 0, fontVariantNumeric: "tabular-nums" }}>
        {formatMoney(totalSpent)}
      </span>
    </div>
  );
}

export default function MonthlyReportsPanel() {
  const [yearMonth, setYearMonth] = useState(currentMonthValue());
  const [reports, setReports] = useState([]);
  const [selectedMonth, setSelectedMonth] = useState("");
  const [selectedReport, setSelectedReport] = useState(null);
  const [loadingList, setLoadingList] = useState(true);
  const [generating, setGenerating] = useState(false);
  const [loadingReport, setLoadingReport] = useState(false);
  const [deletingMonth, setDeletingMonth] = useState(null);
  const [error, setError] = useState("");

  const selectedSummary = useMemo(() => {
    if (!selectedReport) return "";
    return formatMonth(selectedReport.year_month);
  }, [selectedReport]);

  const maxCategorySpend = useMemo(() => {
    if (!selectedReport?.category_breakdown?.length) return 0;
    return Math.max(...selectedReport.category_breakdown.map((r) => r.total_spent || 0));
  }, [selectedReport]);

  const totalEstimatedSavings = useMemo(() => {
    if (!selectedReport?.suggestions?.length) return 0;
    return selectedReport.suggestions.reduce((s, sg) => s + Number(sg.estimated_savings_usd || 0), 0);
  }, [selectedReport]);

  const loadReports = async () => {
    setLoadingList(true);
    setError("");
    try {
      const data = await monthlyReportsApi.list();
      const normalized = asArray(data).map(normalizeReport).filter((r) => r.year_month);
      setReports(normalized);
      if (normalized.length > 0) {
        setSelectedMonth((prev) => prev || normalized[0].year_month);
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setLoadingList(false);
    }
  };

  const loadSingleReport = async (month) => {
    if (!month) return;
    setLoadingReport(true);
    setError("");
    try {
      const data = await monthlyReportsApi.get(month);
      setSelectedReport(normalizeReport(data));
      setSelectedMonth(month);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoadingReport(false);
    }
  };

  useEffect(() => { loadReports(); }, []);

  useEffect(() => {
    if (selectedMonth) loadSingleReport(selectedMonth);
    else setSelectedReport(null);
  }, [selectedMonth]);

  const handleDelete = async (month, e) => {
    e.stopPropagation();
    setDeletingMonth(month);
    setError("");
    try {
      await monthlyReportsApi.delete(month);
      if (selectedMonth === month) { setSelectedMonth(""); setSelectedReport(null); }
      await loadReports();
    } catch (err) {
      setError(err.message);
    } finally {
      setDeletingMonth(null);
    }
  };

  const handleGenerate = async () => {
    setGenerating(true);
    setError("");
    try {
      const created = normalizeReport(await monthlyReportsApi.generate(yearMonth));
      await loadReports();
      setSelectedMonth(created.year_month);
      setSelectedReport(created);
    } catch (err) {
      setError(err.message);
    } finally {
      setGenerating(false);
    }
  };

  const handleDownloadPdf = () => {
    if (!selectedReport) return;
    const doc = new jsPDF();
    const left = 15;
    const maxWidth = 180;
    let y = 20;

    doc.setFontSize(18);
    doc.text(`AlloyFinance Monthly Report — ${formatMonth(selectedReport.year_month)}`, left, y);
    y += 10;

    doc.setFontSize(12);
    doc.text(`Total spent: ${formatMoney(selectedReport.total_spent)}`, left, y);
    y += 6;
    doc.text(`Generated: ${formatGeneratedAt(selectedReport.generated_at)}`, left, y);
    y += 10;

    if (selectedReport.headline) {
      doc.setFontSize(13);
      doc.setFont(undefined, "bold");
      doc.text("Key Takeaway", left, y);
      y += 7;
      doc.setFont(undefined, "normal");
      doc.setFontSize(11);
      const hLines = doc.splitTextToSize(selectedReport.headline, maxWidth);
      doc.text(hLines, left, y);
      y += hLines.length * 6 + 6;
    }

    doc.setFontSize(13);
    doc.setFont(undefined, "bold");
    doc.text("Summary", left, y);
    y += 7;
    doc.setFont(undefined, "normal");
    doc.setFontSize(11);
    const commentsLines = doc.splitTextToSize(selectedReport.comments || "", maxWidth);
    doc.text(commentsLines, left, y);
    y += commentsLines.length * 6 + 6;

    doc.setFontSize(13);
    doc.setFont(undefined, "bold");
    doc.text("Action Items", left, y);
    y += 7;
    doc.setFont(undefined, "normal");
    doc.setFontSize(11);
    (selectedReport.suggestions || []).forEach((s, idx) => {
      const label = `${idx + 1}. ${s.title}${s.estimated_savings_usd > 0 ? ` — save ~${formatMoney(s.estimated_savings_usd)}/mo` : ""}`;
      const lines = doc.splitTextToSize(label, maxWidth);
      doc.text(lines, left, y);
      y += lines.length * 6;
      if (s.rationale) {
        const rLines = doc.splitTextToSize(`   ${s.rationale}`, maxWidth);
        doc.text(rLines, left, y);
        y += rLines.length * 6 + 2;
      }
    });

    y += 4;
    doc.setFontSize(13);
    doc.setFont(undefined, "bold");
    doc.text("Spending by Category", left, y);
    y += 7;
    doc.setFont(undefined, "normal");
    doc.setFontSize(11);
    (selectedReport.category_breakdown || []).slice(0, 8).forEach((row) => {
      doc.text(`${row.category}: ${formatMoney(row.total_spent)}`, left, y);
      y += 6;
    });

    doc.save(`monthly-report-${selectedReport.year_month}.pdf`);
  };

  return (
    <div className="monthly-panel">
      {/* Generate row */}
      <section className="card">
        <div className="table-header">
          <div>
            <h2>Generate Monthly Report</h2>
            <p className="header-sub" style={{ marginTop: "0.2rem" }}>AI-powered analysis of your spending — patterns, trends, and specific actions.</p>
          </div>
        </div>
        <div className="monthly-generate-row">
          <label className="field monthly-month-field">
            <span>Month</span>
            <input type="month" value={yearMonth} onChange={(e) => setYearMonth(e.target.value)} />
          </label>
          <button className="btn btn-primary" onClick={handleGenerate} disabled={generating || !yearMonth}>
            {generating ? "Analyzing…" : "Generate report"}
          </button>
        </div>
        {error && <p className="global-error" style={{ marginTop: "0.75rem" }}>{error}</p>}
      </section>

      {/* History list */}
      <section className="card">
        <div className="table-header">
          <h2>Previous Reports</h2>
          <span className="txn-count">{reports.length} saved</span>
        </div>
        {loadingList ? (
          <p className="table-status">Loading reports…</p>
        ) : reports.length === 0 ? (
          <p className="table-status">No reports yet. Generate your first report above.</p>
        ) : (
          <div className="reports-list">
            {reports.map((report) => (
              <div
                key={report.year_month}
                className={`report-list-item ${selectedMonth === report.year_month ? "active" : ""}`}
                onClick={() => setSelectedMonth(report.year_month)}
              >
                <span style={{ fontWeight: 500 }}>{formatMonth(report.year_month)}</span>
                <span style={{ display: "flex", alignItems: "center", gap: "0.75rem" }}>
                  <span style={{ color: "#dc2626", fontWeight: 600, fontVariantNumeric: "tabular-nums" }}>{formatMoney(report.total_spent)}</span>
                  <button
                    className="btn btn-filter"
                    style={{ fontSize: "0.75rem", padding: "0.25rem 0.6rem", color: "#b91c1c" }}
                    disabled={deletingMonth === report.year_month}
                    onClick={(e) => handleDelete(report.year_month, e)}
                  >
                    {deletingMonth === report.year_month ? "…" : "Delete"}
                  </button>
                </span>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Report detail */}
      <section className="card">
        <div className="table-header">
          <h2>{selectedSummary || "Report Details"}</h2>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            {selectedReport && (
              <span style={{ fontSize: "0.78rem", color: "#94a3b8" }}>
                Generated {formatGeneratedAt(selectedReport.generated_at)}
              </span>
            )}
            <button className="btn btn-filter" disabled={!selectedReport} onClick={handleDownloadPdf}>
              Download PDF
            </button>
          </div>
        </div>

        {loadingReport ? (
          <p className="table-status">Loading report…</p>
        ) : !selectedReport ? (
          <p className="table-status">Select a report to view it.</p>
        ) : (
          <div className="report-content">

            {/* KPI row */}
            <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: "0.75rem" }}>
              <div className="report-kpi" style={{ borderColor: "#fca5a5", background: "#fff1f2" }}>
                <p className="txn-count" style={{ color: "#991b1b" }}>Total spent</p>
                <h3 style={{ color: "#dc2626" }}>{formatMoney(selectedReport.total_spent)}</h3>
              </div>
              <div className="report-kpi" style={{ borderColor: "#a5f3fc", background: "#ecfeff" }}>
                <p className="txn-count" style={{ color: "#155e75" }}>Categories tracked</p>
                <h3 style={{ color: "#0e7490" }}>{selectedReport.category_breakdown.length}</h3>
              </div>
              {totalEstimatedSavings > 0 && (
                <div className="report-kpi" style={{ borderColor: "#86efac", background: "#f0fdf4" }}>
                  <p className="txn-count" style={{ color: "#166534" }}>Potential monthly savings</p>
                  <h3 style={{ color: "#16a34a" }}>{formatMoney(totalEstimatedSavings)}</h3>
                </div>
              )}
            </div>

            {/* Headline callout */}
            {selectedReport.headline && (
              <div style={{
                background: "linear-gradient(135deg, #eef2ff 0%, #f5f3ff 100%)",
                border: "1px solid #c7d2fe",
                borderRadius: "12px",
                padding: "0.9rem 1rem",
                display: "flex",
                alignItems: "flex-start",
                gap: "0.6rem",
              }}>
                <span style={{ fontSize: "1.2rem", flexShrink: 0 }}>💡</span>
                <p style={{ margin: 0, color: "#3730a3", fontWeight: 600, fontSize: "0.95rem", lineHeight: 1.4 }}>
                  {selectedReport.headline}
                </p>
              </div>
            )}

            {/* Summary */}
            <div>
              <h4>Monthly Summary</h4>
              <p style={{ color: "#334155", lineHeight: 1.6 }}>{selectedReport.comments || "No summary available."}</p>
            </div>

            {/* Watch items */}
            {selectedReport.watch_items.length > 0 && (
              <div>
                <h4>Things to Watch</h4>
                <div style={{ display: "flex", flexDirection: "column", gap: "0.4rem" }}>
                  {selectedReport.watch_items.map((item, idx) => (
                    <div key={idx} style={{
                      display: "flex",
                      gap: "0.55rem",
                      alignItems: "flex-start",
                      background: "#fffbeb",
                      border: "1px solid #fde68a",
                      borderRadius: "9px",
                      padding: "0.6rem 0.75rem",
                    }}>
                      <span style={{ fontSize: "0.9rem", flexShrink: 0, marginTop: "0.05rem" }}>⚠️</span>
                      <span style={{ fontSize: "0.88rem", color: "#78350f", lineHeight: 1.4 }}>{item}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Action items */}
            <div>
              <h4>Action Items</h4>
              {selectedReport.suggestions.length === 0 ? (
                <p className="table-status">No suggestions available.</p>
              ) : (
                <div style={{ display: "flex", flexDirection: "column", gap: "0.65rem" }}>
                  {selectedReport.suggestions.map((s, idx) => (
                    <div key={idx} style={{
                      border: "1px solid var(--border)",
                      borderRadius: "12px",
                      padding: "0.85rem 1rem",
                      background: "#fff",
                      display: "flex",
                      flexDirection: "column",
                      gap: "0.4rem",
                    }}>
                      <div style={{ display: "flex", alignItems: "center", gap: "0.55rem", flexWrap: "wrap" }}>
                        <span style={{
                          width: "22px", height: "22px", borderRadius: "50%",
                          background: "#6366f1", color: "#fff",
                          fontSize: "0.75rem", fontWeight: 700,
                          display: "flex", alignItems: "center", justifyContent: "center",
                          flexShrink: 0,
                        }}>{idx + 1}</span>
                        <strong style={{ fontSize: "0.92rem", color: "#1e293b" }}>{s.title}</strong>
                        <DifficultyBadge difficulty={s.difficulty} />
                        <ImpactBadge impact={s.impact} />
                        {s.estimated_savings_usd > 0 && (
                          <span style={{
                            marginLeft: "auto", fontSize: "0.82rem", fontWeight: 700,
                            color: "#16a34a", background: "#f0fdf4",
                            padding: "0.2rem 0.55rem", borderRadius: "99px",
                            border: "1px solid #86efac", flexShrink: 0,
                          }}>
                            Save ~{formatMoney(s.estimated_savings_usd)}/mo
                          </span>
                        )}
                      </div>
                      {s.rationale && (
                        <p style={{ margin: 0, fontSize: "0.85rem", color: "#475569", lineHeight: 1.5, paddingLeft: "1.9rem" }}>
                          {s.rationale}
                        </p>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>

            {/* Category breakdown */}
            {selectedReport.category_breakdown.length > 0 && (
              <div>
                <h4>Spending by Category</h4>
                <div style={{ marginTop: "0.5rem" }}>
                  {selectedReport.category_breakdown.slice(0, 8).map((row, idx) => (
                    <CategoryBar
                      key={row.category || idx}
                      category={row.category || "other"}
                      totalSpent={row.total_spent || 0}
                      maxSpent={maxCategorySpend}
                      index={idx}
                    />
                  ))}
                </div>
              </div>
            )}

            {/* Top merchants */}
            {selectedReport.merchant_breakdown.length > 0 && (
              <div>
                <h4>Top Merchants</h4>
                <ul className="report-list">
                  {selectedReport.merchant_breakdown.slice(0, 6).map((row, idx) => (
                    <li key={`${row?.merchant_name || "unknown"}-${idx}`}>
                      <span style={{ color: "#334155" }}>{row?.merchant_name || "Unknown"}</span>
                      <span style={{ display: "flex", alignItems: "center", gap: "0.6rem" }}>
                        {row?.transaction_count != null && (
                          <span style={{ fontSize: "0.78rem", color: "#94a3b8" }}>{row.transaction_count} txns</span>
                        )}
                        <strong style={{ color: "#1e293b", fontVariantNumeric: "tabular-nums" }}>{formatMoney(row?.total_spent)}</strong>
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}

          </div>
        )}
      </section>
    </div>
  );
}
