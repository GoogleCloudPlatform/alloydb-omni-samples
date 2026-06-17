const BASE_URL = import.meta.env?.VITE_API_URL || "http://localhost:8000";

function authHeaders() {
  const token = localStorage.getItem("token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export const aiAnalysisApi = {
  nl2sql: async (query) => {
    const res = await fetch(`${BASE_URL}/api/ai-analysis/nl2sql`, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...authHeaders() },
      body: JSON.stringify({ query }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || `HTTP ${res.status}`);
    }
    return res.json();
  },
};

export async function analyzeWithNl2Sql(question, { analysisClient = aiAnalysisApi } = {}) {
  return analysisClient.nl2sql(question.trim());
}
