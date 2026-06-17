import { useState, useEffect, useCallback } from "react";
import { transactionsApi } from "../api/transactions";

export function useTransactions() {
  const [transactions, setTransactions] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [activeFilter, setActiveFilter] = useState("recent");

  const fetchRecent = useCallback(async (limit = 20) => {
    setLoading(true);
    setError(null);
    try {
      const data = await transactionsApi.getRecent(limit);
      setTransactions(data);
      setActiveFilter("recent");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchByCategory = useCallback(async (category) => {
    setLoading(true);
    setError(null);
    try {
      const data = await transactionsApi.getByCategory(category);
      setTransactions(data);
      setActiveFilter(`category:${category}`);
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchCurrentMonth = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await transactionsApi.getCurrentMonth();
      setTransactions(data);
      setActiveFilter("current-month");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchPreviousMonth = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await transactionsApi.getPreviousMonth();
      setTransactions(data);
      setActiveFilter("previous-month");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchAll = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await transactionsApi.getAll();
      setTransactions(data);
      setActiveFilter("all-time");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  const createTransaction = useCallback(async (txn) => {
    const created = await transactionsApi.create(txn);
    setTransactions((prev) => [created, ...prev]);
    return created;
  }, []);

  useEffect(() => {
    fetchRecent();
  }, [fetchRecent]);

  return {
    transactions,
    loading,
    error,
    activeFilter,
    fetchRecent,
    fetchByCategory,
    fetchCurrentMonth,
    fetchPreviousMonth,
    fetchAll,
    createTransaction,
  };
}
