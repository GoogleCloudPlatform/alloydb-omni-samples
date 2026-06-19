const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  const headers = {};
  if (token) headers["Authorization"] = `Bearer ${token}`;
  return headers;
}

export const transactionsApi = {
  create: async (txn) => {
    const res = await fetch(`${BASE_URL}/api/transactions`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify(txn),
    });
    if (!res.ok) throw new Error("Failed to create transaction");
    return res.json();
  },

  getByCategory: async (category) => {
    const res = await fetch(`${BASE_URL}/api/transactions/category/${encodeURIComponent(category)}`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch transactions by category");
    return res.json();
  },

  getCurrentMonth: async () => {
    const res = await fetch(`${BASE_URL}/api/transactions/current-month`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch current month transactions");
    return res.json();
  },

  getPreviousMonth: async () => {
    const res = await fetch(`${BASE_URL}/api/transactions/previous-month`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch previous month transactions");
    return res.json();
  },

  getRecent: async (limit = 20) => {
    const res = await fetch(`${BASE_URL}/api/transactions/recent?limit=${limit}`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch recent transactions");
    return res.json();
  },

  getAll: async () => {
    const res = await fetch(`${BASE_URL}/api/transactions/all`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch all transactions");
    return res.json();
  },

  getCategories: async () => {
    const res = await fetch(`${BASE_URL}/api/transactions/categories`, {
      headers: authHeaders(),
    });
    if (!res.ok) throw new Error("Failed to fetch categories");
    return res.json();
  },
};
