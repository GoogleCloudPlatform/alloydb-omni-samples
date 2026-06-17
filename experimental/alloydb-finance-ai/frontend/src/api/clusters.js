const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

function timeParams({ timeRange, startDate, endDate } = {}) {
  const p = {};
  if (timeRange && timeRange !== "all") p.time_range = timeRange;
  if (startDate) p.start_date = startDate;
  if (endDate) p.end_date = endDate;
  return p;
}

export const clustersApi = {
  getClusters: async ({ k = 5, compare = false, timeRange, startDate, endDate } = {}) => {
    const params = new URLSearchParams({ k, compare, ...timeParams({ timeRange, startDate, endDate }) });
    const res = await fetch(`${BASE_URL}/api/transactions/clusters?${params}`, { headers: authHeaders() });
    if (!res.ok) {
      const e = await res.json().catch(() => ({}));
      throw new Error(e.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },

  semanticFilter: async ({ category, threshold = 0.75, limit = 50, timeRange, startDate, endDate } = {}) => {
    const params = new URLSearchParams({ category, threshold, limit, ...timeParams({ timeRange, startDate, endDate }) });
    const res = await fetch(`${BASE_URL}/api/transactions/semantic-filter?${params}`, { headers: authHeaders() });
    if (!res.ok) {
      const e = await res.json().catch(() => ({}));
      throw new Error(e.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },
};
