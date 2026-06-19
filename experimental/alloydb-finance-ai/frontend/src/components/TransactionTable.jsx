import { useState } from "react";
import { catPalette } from "../utils/categoryColors";

const PAGE_SIZE = 10;

function formatDate(iso) {
  return new Date(iso).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

function formatAmount(amount) {
  const num = typeof amount === "string" ? parseFloat(amount) : amount;
  const formatted = Math.abs(num).toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
  });
  return (
    <span className={num < 0 ? "amount-neg" : "amount-neutral"}>
      {num < 0 ? `-${formatted}` : formatted}
    </span>
  );
}

function categoryBadge(category) {
  const { bg, text } = catPalette(category);
  return (
    <span className="badge" style={{ background: bg, color: text }}>
      {category}
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
      {text.toLowerCase()}
    </td>
  );
}

export default function TransactionTable({ transactions, loading }) {
  const [page, setPage] = useState(0);

  if (loading) {
    return <p className="table-status">Loading transactions...</p>;
  }

  if (transactions.length === 0) {
    return <p className="table-status">No transactions found for this filter.</p>;
  }

  const totalPages = Math.ceil(transactions.length / PAGE_SIZE);
  const clampedPage = Math.min(page, totalPages - 1);
  const pageSlice = transactions.slice(clampedPage * PAGE_SIZE, (clampedPage + 1) * PAGE_SIZE);

  return (
    <div>
      <div className="table-wrap">
        <table className="txn-table">
          <thead>
            <tr>
              <th>Date</th>
              <th>Merchant</th>
              <th>Category</th>
              <th>Amount</th>
              <th>Qty</th>
              <th>Country</th>
              <th>Description</th>
            </tr>
          </thead>
          <tbody>
            {pageSlice.map((t) => (
              <tr key={t.transaction_id}>
                <td className="cell-date">{formatDate(t.timestamp)}</td>
                <td className="cell-merchant">{t.merchant_name}</td>
                <td>{categoryBadge(t.spending_category)}</td>
                <td className="cell-amount">{formatAmount(t.amount)}</td>
                <td>{t.quantity ?? "—"}</td>
                <td>{t.country || "—"}</td>
                <DescCell text={t.description} />
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
    </div>
  );
}
