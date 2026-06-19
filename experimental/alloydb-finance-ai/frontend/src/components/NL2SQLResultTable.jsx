import { useState } from "react";
import { catPalette } from "../utils/categoryColors";

const PAGE_SIZE = 10;

const HIDDEN_COLS = new Set(["transaction_id", "user_id", "embedding"]);

const DATE_COLS = new Set(["timestamp", "date", "created_at", "updated_at"]);
const AMOUNT_COLS = new Set(["amount", "total", "total_amount", "sum", "avg", "average"]);
const CATEGORY_COLS = new Set(["spending_category", "category"]);
const DESC_COLS = new Set(["description", "item_description"]);

function displayName(col) {
  if (col === "item_description") return "description";
  return col;
}

function formatDate(val) {
  const d = new Date(val);
  if (isNaN(d)) return String(val);
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

function formatAmount(val) {
  const num = typeof val === "string" ? parseFloat(val) : val;
  if (isNaN(num)) return String(val);
  const formatted = Math.abs(num).toLocaleString("en-US", { style: "currency", currency: "USD" });
  return (
    <span className={num < 0 ? "amount-neg" : "amount-neutral"}>
      {num < 0 ? `-${formatted}` : formatted}
    </span>
  );
}

function categoryBadge(val) {
  if (!val) return "—";
  const { bg, text } = catPalette(val);
  return (
    <span className="badge" style={{ background: bg, color: text }}>
      {val}
    </span>
  );
}

function DescCell({ text }) {
  const [expanded, setExpanded] = useState(false);
  if (!text) return <td className="cell-desc">{"—"}</td>;
  return (
    <td
      className={`cell-desc${expanded ? " cell-desc-expanded" : ""}`}
      onClick={() => setExpanded((e) => !e)}
      title={expanded ? "Click to collapse" : "Click to expand"}
    >
      {String(text).toLowerCase()}
    </td>
  );
}

function renderCell(col, val) {
  const key = col.toLowerCase();
  if (val === null || val === undefined) return <span className="nl-null">—</span>;
  if (DATE_COLS.has(key)) return formatDate(val);
  if (AMOUNT_COLS.has(key)) return formatAmount(val);
  if (CATEGORY_COLS.has(key)) return categoryBadge(String(val));
  return String(val);
}

export default function NL2SQLResultTable({ result }) {
  const [page, setPage] = useState(0);

  if (!result) return null;

  const allColumns = result.columns || [];
  const allRows = result.rows || [];

  // Filter out internal columns and track which indices to keep
  const visibleIndices = allColumns
    .map((col, i) => ({ col, i }))
    .filter(({ col }) => !HIDDEN_COLS.has(col.toLowerCase()));
  const columns = visibleIndices.map(({ col }) => col);

  if (allRows.length === 0) return <p className="table-status">No rows returned.</p>;

  const rows = allRows.map((row) => visibleIndices.map(({ i }) => row[i]));

  const totalPages = Math.ceil(rows.length / PAGE_SIZE);
  const clampedPage = Math.min(page, totalPages - 1);
  const pageSlice = rows.slice(clampedPage * PAGE_SIZE, (clampedPage + 1) * PAGE_SIZE);

  return (
    <>
      {result.truncated && (
        <p className="nl-truncated">Showing first 200 rows — refine your query to see fewer results.</p>
      )}
      <div className="table-wrap">
        <table className="txn-table">
          <thead>
            <tr>
              {columns.map((col) => (
                <th key={col}>{displayName(col)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pageSlice.map((row, i) => (
              <tr key={i}>
                {row.map((cell, j) =>
                  DESC_COLS.has(columns[j].toLowerCase()) ? (
                    <DescCell key={j} text={cell != null ? String(cell) : ""} />
                  ) : (
                    <td key={j}>{renderCell(columns[j], cell)}</td>
                  )
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {totalPages > 1 && (
        <div className="pagination-row">
          <button
            className="btn btn-filter"
            onClick={() => setPage((p) => Math.max(p - 1, 0))}
            disabled={clampedPage === 0}
          >
            ← Prev
          </button>
          <span className="pagination-info">{clampedPage + 1} / {totalPages}</span>
          <button
            className="btn btn-filter"
            onClick={() => setPage((p) => Math.min(p + 1, totalPages - 1))}
            disabled={clampedPage >= totalPages - 1}
          >
            Next →
          </button>
        </div>
      )}
    </>
  );
}
