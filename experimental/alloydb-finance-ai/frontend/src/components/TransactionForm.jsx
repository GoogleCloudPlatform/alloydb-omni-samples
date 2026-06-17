import { useState } from "react";

const INITIAL = {
  amount: "",
  merchant_name: "",
  spending_category: "",
  quantity: 1,
  country: "United Kingdom",
  item_description: "",
};

export default function TransactionForm({ onSubmit, categories = [] }) {
  const [form, setForm] = useState(INITIAL);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState(null);

  const set = (field) => (e) =>
    setForm((prev) => ({ ...prev, [field]: e.target.value }));

  const handleSubmit = async (e) => {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        ...form,
        amount: parseFloat(form.amount),
        quantity: parseInt(form.quantity, 10) || 1,
        item_description: form.item_description || null,
      });
      setForm(INITIAL);
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form className="txn-form" onSubmit={handleSubmit}>
      <h2>Add Transaction</h2>

      {error && <p className="form-error">{error}</p>}

      <div className="form-grid">
        <label className="field">
          <span>Amount</span>
          <input
            type="number"
            step="0.01"
            min="0"
            required
            placeholder="42.50"
            value={form.amount}
            onChange={set("amount")}
          />
        </label>

        <label className="field">
          <span>Merchant Name</span>
          <input
            type="text"
            required
            placeholder="Tesco"
            value={form.merchant_name}
            onChange={set("merchant_name")}
          />
        </label>

        <label className="field">
          <span>Spending Category</span>
          <select value={form.spending_category} onChange={set("spending_category")} required>
            <option value="" disabled>Select a category</option>
            {categories.map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
        </label>

        <label className="field">
          <span>Quantity</span>
          <input
            type="number"
            min="1"
            step="1"
            required
            placeholder="1"
            value={form.quantity}
            onChange={set("quantity")}
          />
        </label>

        <label className="field">
          <span>Country</span>
          <input
            type="text"
            required
            placeholder="United Kingdom"
            value={form.country}
            onChange={set("country")}
          />
        </label>

        <label className="field field-wide">
          <span>Item Description</span>
          <input
            type="text"
            placeholder="Item description (used for semantic search)"
            value={form.item_description}
            onChange={set("item_description")}
          />
        </label>
      </div>

      <button type="submit" className="btn btn-primary" disabled={submitting}>
        {submitting ? "Adding..." : "Add Transaction"}
      </button>
    </form>
  );
}
