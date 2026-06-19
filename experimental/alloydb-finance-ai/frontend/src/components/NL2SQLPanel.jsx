import { useState } from "react";
import ReactMarkdown from "react-markdown";
import { analyzeWithNl2Sql } from "../api/aiAnalysis";
import NL2SQLResultTable from "./NL2SQLResultTable";

function stripJsonBlocks(text) {
  return text.replace(/```json[\s\S]*?```/gi, "").replace(/```[\s\S]*?```/g, "").trim();
}

const CHIPS = [
  "Show total spending by category",
  "Top 5 merchants by total spend",
  "What did I spend on food last month?",
  "Show all transactions from United Kingdom",
  "Which categories have the most transactions?",
  "What is my average transaction amount by category?",
  "Show my most expensive purchases",
];

export default function NL2SQLPanel() {
  const [question, setQuestion] = useState("");
  const [result, setResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const handleAsk = async () => {
    const q = question.trim();
    if (!q) return;
    setLoading(true);
    setError("");
    setResult(null);
    try {
      const data = await analyzeWithNl2Sql(q);
      setResult(data);
    } catch (e) {
      console.error("[NL2SQL] insight generation failed", e);
      setError("I couldn't analyze that question. Try making it more specific, such as asking about spending, categories, merchants, budgets, or time periods.");
    } finally {
      setLoading(false);
    }
  };

  const handleKeyDown = (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && !loading) {
      handleAsk();
    }
  };

  const resultRowCount = result?.rows?.length || 0;

  return (
    <div className="nl-panel">
      <section className="card">
        <div className="table-header">
          <h2>Ask a question</h2>
        </div>

        <textarea
          className="nl-textarea"
          placeholder="e.g. Show total spending by category this month"
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          onKeyDown={handleKeyDown}
          rows={3}
        />
        <p className="nl-hint">Press ⌘ Enter or click Ask question</p>

        <div className="nl-chips">
          {CHIPS.map((chip) => (
            <button
              key={chip}
              className="nl-chip"
              onClick={() => setQuestion(chip)}
            >
              {chip}
            </button>
          ))}
        </div>

        <div style={{ marginTop: "1rem", display: "flex", alignItems: "center", gap: "0.75rem" }}>
          <button
            className="btn btn-primary"
            onClick={handleAsk}
            disabled={!question.trim() || loading}
          >
            {loading ? "Loading..." : "Ask question"}
          </button>
        </div>

        {error && <p className="global-error" style={{ marginTop: "0.75rem" }}>{error}</p>}
      </section>

      {result && (
        <section className="card">
          <div className="table-header">
            <h2>Results</h2>
            <span className="txn-count">
              {resultRowCount} row{resultRowCount !== 1 ? "s" : ""}
              {result?.truncated ? " (truncated)" : ""}
            </span>
          </div>
          {(result.answer || result.insight) && (
            <div className="analysis-insight">
              <h3>Gemini Insight</h3>
              <div className="analysis-answer markdown-body">
                <ReactMarkdown>{stripJsonBlocks(result.answer || result.insight)}</ReactMarkdown>
              </div>
            </div>
          )}
          {result.sql && (
            <details className="sql-details">
              <summary>Generated SQL</summary>
              <pre className="sql-block">{result.sql}</pre>
            </details>
          )}
          <NL2SQLResultTable key={resultRowCount} result={result} />
        </section>
      )}
    </div>
  );
}
