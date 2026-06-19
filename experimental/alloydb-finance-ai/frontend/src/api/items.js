const BASE_URL = process.env.REACT_APP_API_URL || "http://localhost:8000";

export const itemsApi = {
  getAll: async () => {
    const res = await fetch(`${BASE_URL}/items`);
    if (!res.ok) throw new Error("Failed to fetch items");
    return res.json();
  },

  getOne: async (id) => {
    const res = await fetch(`${BASE_URL}/items/${id}`);
    if (!res.ok) throw new Error("Item not found");
    return res.json();
  },

  create: async (item) => {
    const res = await fetch(`${BASE_URL}/items`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(item),
    });
    if (!res.ok) throw new Error("Failed to create item");
    return res.json();
  },

  update: async (id, item) => {
    const res = await fetch(`${BASE_URL}/items/${id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(item),
    });
    if (!res.ok) throw new Error("Failed to update item");
    return res.json();
  },

  delete: async (id) => {
    const res = await fetch(`${BASE_URL}/items/${id}`, { method: "DELETE" });
    if (!res.ok) throw new Error("Failed to delete item");
  },
};