import { useState, useEffect } from "react";
import { itemsApi } from "../api/items";

export function useItems() {
  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  const fetchItems = async () => {
    try {
      setLoading(true);
      const data = await itemsApi.getAll();
      setItems(data);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { fetchItems(); }, []);

  const createItem = async (item) => {
    const newItem = await itemsApi.create(item);
    setItems((prev) => [newItem, ...prev]);
  };

  const deleteItem = async (id) => {
    await itemsApi.delete(id);
    setItems((prev) => prev.filter((i) => i.id !== id));
  };

  return { items, loading, error, createItem, deleteItem, refetch: fetchItems };
}