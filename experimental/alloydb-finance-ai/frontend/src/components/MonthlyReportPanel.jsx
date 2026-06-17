import { useEffect, useMemo, useState } from "react";
import { jsPDF } from "jspdf";
import { monthlyReportApi } from "../api/monthlyReport";

function currentYearMonth() {
  const now = new Date();
  const year = now.getUTCFullYear();
  const month = String(now.getUTCMonth() + 1).padStart(2, "0");
  return `${year}-${month}`;
}

function formatUSD(value) {
  const num = typeof value === "number" ? value : Number(value || 0);
  return num.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

export default function MonthlyReportPanel() {
  const [yearMonth, setYearMonth] = useState(currentYearMonth());
  const [loading, setLoading] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [error, setError] = useState("");
  const [report, setReport] = useState(null);
  const [history, setHistory] = useState([]);
  const [historyMode, setHistoryMode] = useState("month");

  const totals = useMemo(() => report?.totals || null, [report]);
  const monthHistory = useMemo(
    () => history.filter((item) => item.year_month === yearMonth),
    [history, yearMonth],
  );
  const visibleHistory = historyMode === "all" ? history : monthHistory;

  const loadHistory = async () => {
    setHistoryLoading(true);
    setError("");
    try {
      const rows = await monthlyReportApi.listHistory();
      setHistory(rows);
      if (!report) {
        const forMonth = rows.find((item) => item.year_month === yearMonth);
        if (forMonth) setReport(forMonth);
      }
    } catch (err) {
      setError(err.message || "Failed to load report history");
    } finally {
      setHistoryLoading(false);
    }
  };

  useEffect(() => {
    setReport(null);
    loadHistory();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [yearMonth]);

  const handleGenerate = async (force = false) => {
    setLoading(true);
    setError("");
    try {
      const data = await monthlyReportApi.generate(yearMonth, force);
      setReport(data);
      await loadHistory();
    } catch (err) {
      setError(err.message || "Failed to generate monthly report");
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = async (reportId) => {
    try {
      await monthlyReportApi.remove(reportId);
      if (report?.report_id === reportId) {
        setReport(null);
      }
      await loadHistory();
    } catch (err) {
      setError(err.message || "Failed to delete report");
    }
  };

  const handleDownloadPdf = () => {
    if (!report) return;
    const doc = new jsPDF();
    const lines = [];
    lines.push(`AlloyFinance Monthly Report - ${report.year_month}`);
    lines.push(`Generated: ${new Date(report.generated_at).toLocaleString()}`);
    lines.push("");
    lines.push("Summary");
    lines.push(report.summary || "");
    lines.push("");
    lines.push("Key Metrics");
    lines.push(`- Total spent: ${formatUSD(report?.totals?.total_spent || 0)}`);
    lines.push(`- Total income: ${formatUSD(report?.totals?.total_income || 0)}`);
    lines.push(`- Net cashflow: ${formatUSD(report?.totals?.net_cashflow || 0)}`);
    lines.push(`- Transactions: ${report?.totals?.transaction_count || 0}`);
    lines.push("");
    lines.push("Patterns");
    (report.highlights || []).forEach((h) => lines.push(`- ${h}`));
    lines.push("");
    lines.push("Suggestions");
    (report.suggestions || []).forEach((s) => {
      lines.push(`- ${s.title}: ${s.rationale || ""}`);
      lines.push(`  Estimated savings: ${formatUSD(s.estimated_savings_usd || 0)}`);
    });
    const wrapped = doc.splitTextToSize(lines.join("\n"), 180);
    doc.setFontSize(11);
    doc.text(wrapped, 14, 16);
    doc.save(`monthly-report-${report.year_month}.pdf`);
  };

  return (
    <div className="nl-panel">
      <section className="card monthly-create-card">
        <div className="table-header monthly-create-header">
          <div>
            <h2>Create Monthly Report</h2>
            <p className="header-sub monthly-create-sub">
              Pick a month, generate a new AI report, and manage all previously generated reports.
            </p>
          </div>
          <span className="txn-count">{history.length} total saved</span>
        </div>
        <div className="monthly-controls">
          <input
            type="month"
            className="category-select"
            value={yearMonth}
            onChange={(e) => setYearMonth(e.target.value)}
          />
          <button className="btn btn-primary" onClick={() => handleGenerate(false)} disabled={loading}>
            {loading ? "Generating..." : "Generate report"}
          </button>
          <button className="btn btn-filter" onClick={() => handleGenerate(true)} disabled={loading}>
            Regenerate
          </button>
        </div>
        {error && <p className="global-error" style={{ marginTop: "0.75rem" }}>{error}</p>}
      </section>

      <div className="monthly-layout">
        <section className="card monthly-history-card">
          <div className="table-header">
            <h2>Previous Reports</h2>
            <div className="filter-group">
              <button
                className={`btn btn-filter ${historyMode === "month" ? "active" : ""}`}
                onClick={() => setHistoryMode("month")}
              >
                This Month
              </button>
              <button
                className={`btn btn-filter ${historyMode === "all" ? "active" : ""}`}
                onClick={() => setHistoryMode("all")}
              >
                All
              </button>
              <span className="txn-count">
                {visibleHistory.length} shown
              </span>
            </div>
          </div>
          {historyLoading ? (
            <p className="table-status">Loading saved reports...</p>
          ) : visibleHistory.length === 0 ? (
            <p className="table-status">
              {historyMode === "all" ? "No saved reports yet." : "No saved reports for this month yet."}
            </p>
          ) : (
            <div className="monthly-history-list">
              {visibleHistory.map((item) => (
                <article
                  key={item.report_id}
                  className={`monthly-history-item ${report?.report_id === item.report_id ? "is-active" : ""}`}
                >
                  <button
                    className="monthly-history-main"
                    onClick={() => {
                      setYearMonth(item.year_month);
                      setReport(item);
                    }}
                  >
                    <strong>{new Date(item.generated_at).toLocaleString()}</strong>
                    <span className="txn-count">{item.year_month}</span>
                    <p>{item.summary}</p>
                  </button>
                  <div className="filter-group">
                    <button className="btn btn-filter" onClick={() => setReport(item)}>View</button>
                    <button className="btn btn-filter" onClick={() => handleDelete(item.report_id)}>Delete</button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>

        <section className="card monthly-detail-card">
          {!report ? (
            <p className="table-status">Generate or select a report to view details.</p>
          ) : (
            <>
              <div className="table-header">
                <h2>Report Details ({report.year_month})</h2>
                <div className="filter-group">
                  <span className="txn-count">{new Date(report.generated_at).toLocaleString()}</span>
                  <button className="btn btn-filter" onClick={handleDownloadPdf}>Download PDF</button>
                  <button className="btn btn-filter" onClick={() => handleDelete(report.report_id)}>Delete</button>
                </div>
              </div>
              <p className="monthly-summary">{report.summary}</p>
              {totals && (
                <div className="monthly-metrics">
                  <span>Total spent: {formatUSD(totals.total_spent)}</span>
                  <span>Total income: {formatUSD(totals.total_income)}</span>
                  <span>Net cashflow: {formatUSD(totals.net_cashflow)}</span>
                  <span>Transactions: {totals.transaction_count}</span>
                </div>
              )}

              <section className="monthly-detail-section">
                <h3>Patterns</h3>
                {!report.highlights?.length ? (
                  <p className="table-status">No highlights available for this month.</p>
                ) : (
                  <ul className="monthly-list">
                    {report.highlights.map((h, idx) => (
                      <li key={`${idx}-${h}`}>{h}</li>
                    ))}
                  </ul>
                )}
              </section>

              <section className="monthly-detail-section">
                <h3>Suggestions</h3>
                {!report.suggestions?.length ? (
                  <p className="table-status">No suggestion needed for this month.</p>
                ) : (
                  <div className="monthly-suggestions">
                    {report.suggestions.map((s, idx) => (
                      <article key={`${idx}-${s.title}`} className="monthly-suggestion-card">
                        <h3>{s.title}</h3>
                        {s.rationale && <p>{s.rationale}</p>}
                        <span className="txn-count">
                          Estimated savings: {formatUSD(s.estimated_savings_usd || 0)}
                        </span>
                      </article>
                    ))}
                  </div>
                )}
              </section>
            </>
          )}
        </section>
      </div>
    </div>
  );
}
