import { useState } from "react";
import { customerApi } from "../api/customer";
import TransactionTable from "./TransactionTable";

const CHIPS = [
  "How much did I spend on food last month?",
  "Find transactions similar to my gym membership",
  "Summarize my spending for January",
  "Cluster my merchants into spending groups",
  "Hello!",
];

const TOOL_LABELS = {
  nl2sql: "NL2SQL — structured questions over your data",
  semantic_search: "Semantic Search — find transactions by meaning",
  monthly_reports: "Monthly Reports — month-level summaries",
  clustering: "Clustering — group or segment patterns",
  general: "General — not routed to a specific tool",
};

export default function CustomerPanel() {
  const [message, setMessage] = useState("");
  const [result, setResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const handleRoute = async () => {
    const m = message.trim();
    if (!m) return;
    setLoading(true);
    setError("");
    setResult(null);
    try {
      const data = await customerApi.ask(m);
      setResult(data);
    } catch (e) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  };

  const handleKeyDown = (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      handleRoute();
    }
  };

  return (
    <div className="nl-panel">
      <section className="card">
        <div className="table-header">
          <h2>Insights</h2>
        </div>
        <p style={{ color: "#64748b", marginBottom: "0.75rem", fontSize: "0.9rem" }}>
          Describe what you want in plain language. We route to a tool and execute it in one step.
        </p>

        <textarea
          className="nl-textarea"
          placeholder="e.g. Top merchants by spend this quarter"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={handleKeyDown}
          rows={3}
        />
        <p className="nl-hint">Press ⌘ Enter or click Ask</p>

        <div className="nl-chips">
          {CHIPS.map((chip) => (
            <button key={chip} type="button" className="nl-chip" onClick={() => setMessage(chip)}>
              {chip}
            </button>
          ))}
        </div>

        <div style={{ marginTop: "1rem", display: "flex", alignItems: "center", gap: "0.75rem" }}>
          <button
            type="button"
            className="btn btn-primary"
            onClick={handleRoute}
            disabled={!message.trim() || loading}
          >
            {loading ? "Running…" : "Ask"}
          </button>
        </div>

        {error && <p className="global-error" style={{ marginTop: "0.75rem" }}>{error}</p>}
      </section>

      {result && (
        <section className="card">
          <div className="table-header"><h2>Result</h2></div>
          <p style={{ fontSize: "1.05rem", marginBottom: "0.5rem" }}><strong>{result.tool}</strong></p>
          <p style={{ color: "#64748b", marginBottom: "0.75rem" }}>
            {TOOL_LABELS[result.tool] || TOOL_LABELS.general}
          </p>
          {result.reasoning && (
            <p style={{ fontSize: "0.875rem", color: "#94a3b8" }}>
              <span style={{ fontWeight: 600, color: "#64748b" }}>Model note:</span>{" "}
              {result.reasoning}
            </p>
          )}

          {result.tool === "nl2sql" && result.result && (
            <>
              <p style={{ marginTop: "0.75rem", color: "#64748b", fontSize: "0.9rem" }}>
                SQL: <code>{result.result.sql}</code>
              </p>
              {result.result.truncated && (
                <p className="nl-truncated">Showing first 200 rows - refine your query to see fewer results.</p>
              )}
              {result.result.rows?.length === 0 ? (
                <p className="table-status">No rows returned.</p>
              ) : (
                <div className="table-wrap" style={{ marginTop: "0.75rem" }}>
                  <table className="txn-table">
                    <thead>
                      <tr>
                        {(result.result.columns || []).map((col) => (
                          <th key={col}>{col}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {(result.result.rows || []).map((row, i) => (
                        <tr key={i}>
                          {row.map((cell, j) => (
                            <td key={j}>{cell === null || cell === undefined ? "null" : String(cell)}</td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}

          {result.tool === "semantic_search" && (
            <div style={{ marginTop: "0.75rem" }}>
              <TransactionTable transactions={result.result?.matches || []} loading={false} />
            </div>
          )}

          {result.tool === "monthly_reports" && result.result && (
            <div style={{ marginTop: "0.75rem" }}>
              <p><strong>Month:</strong> {result.result.year_month}</p>
              <p><strong>Total Spent:</strong> ${Number(result.result.total_spent || 0).toLocaleString()}</p>
              <p>{result.result.comments}</p>
              {Array.isArray(result.result.suggestions) && result.result.suggestions.length > 0 && (
                <ul style={{ marginTop: "0.5rem", paddingLeft: "1rem" }}>
                  {result.result.suggestions.map((s, idx) => (
                    <li key={`${idx}-${s}`}>{s}</li>
                  ))}
                </ul>
              )}
            </div>
          )}

          {result.tool === "clustering" && result.result?.message && (
            <p style={{ marginTop: "0.75rem", color: "#64748b" }}>{result.result.message}</p>
          )}

          {result.tool === "general" && result.result?.message && (
            <p style={{ marginTop: "0.75rem", color: "#64748b" }}>{result.result.message}</p>
          )}
        </section>
      )}
    </div>
  );
}
