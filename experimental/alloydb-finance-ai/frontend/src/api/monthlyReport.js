const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export const monthlyReportApi = {
  async get(yearMonth) {
    const params = new URLSearchParams();
    if (yearMonth) params.set("year_month", yearMonth);
    const res = await fetch(`${BASE_URL}/api/agents/monthly-report?${params.toString()}`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },

  async generate(yearMonth, force = false) {
    const res = await fetch(`${BASE_URL}/api/agents/monthly-report/generate`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ year_month: yearMonth, force }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },

  async listHistory(yearMonth) {
    const params = new URLSearchParams();
    if (yearMonth) params.set("year_month", yearMonth);
    const res = await fetch(`${BASE_URL}/api/agents/monthly-report/history?${params.toString()}`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },

  async remove(reportId) {
    const res = await fetch(`${BASE_URL}/api/agents/monthly-report/${encodeURIComponent(reportId)}`, {
      method: "DELETE",
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
  },
};
