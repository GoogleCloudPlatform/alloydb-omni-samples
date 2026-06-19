const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export const monthlyReportsApi = {
  generate: async (yearMonth) => {
    const res = await fetch(`${BASE_URL}/api/reports/monthly/generate`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ year_month: yearMonth || null }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Failed to generate monthly report");
    }
    return res.json();
  },

  list: async () => {
    const res = await fetch(`${BASE_URL}/api/reports/monthly`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Failed to load monthly reports");
    }
    return res.json();
  },

  get: async (yearMonth) => {
    const res = await fetch(`${BASE_URL}/api/reports/monthly/${encodeURIComponent(yearMonth)}`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Failed to load selected report");
    }
    return res.json();
  },

  delete: async (yearMonth) => {
    const res = await fetch(`${BASE_URL}/api/reports/monthly/${encodeURIComponent(yearMonth)}`, {
      method: "DELETE",
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Failed to delete report");
    }
  },
};
