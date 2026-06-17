const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export const customerApi = {
  /**
   * @param {string} message
   * @returns {Promise<{ tool: string, reasoning?: string | null }>}
   */
  routeTool: async (message) => {
    const res = await fetch(`${BASE_URL}/api/customer/route-tool`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ message }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },

  /**
   * Route and execute the selected tool server-side.
   * @param {string} message
   */
  ask: async (message) => {
    const res = await fetch(`${BASE_URL}/api/customer/ask`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ message }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },
};
