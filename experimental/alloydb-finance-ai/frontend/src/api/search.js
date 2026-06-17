const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export const searchApi = {
  search: async (q, limit = 50) => {
    const params = new URLSearchParams({ q, limit });
    const res = await fetch(`${BASE_URL}/api/transactions/search?${params}`, {
      headers: authHeaders(),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },
};
