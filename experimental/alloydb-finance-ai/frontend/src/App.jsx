import { useState, useEffect, useCallback, useMemo } from "react";
import { Routes, Route, Link } from "react-router-dom";
import { useTransactions } from "./hooks/useTransactions";
import TransactionForm from "./components/TransactionForm";
import TransactionTable from "./components/TransactionTable";
import NL2SQLPanel from "./components/NL2SQLPanel";
import CustomerPanel from "./components/CustomerPanel";
import MonthlyReportsPanel from "./components/MonthlyReportsPanel";
import SemanticClusters from "./components/SemanticClusters";
import PerformanceDashboard from "./components/PerformanceDashboard";
import { searchApi } from "./api/search";
import { transactionsApi } from "./api/transactions";
import { catBarColor } from "./utils/categoryColors";

const BASE_URL = import.meta.env.VITE_API_URL || "http://localhost:8000";

function catColor(cat) { return catBarColor(cat); }
function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }
function fmt(n) {
  return Math.abs(n).toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function useAuth() {
  const [user, setUser] = useState(null);
  const [loading, setLoading] = useState(true);

  const checkAuth = useCallback(async () => {
    const token = localStorage.getItem("token");
    if (!token) { setLoading(false); return; }
    try {
      const res = await fetch(`${BASE_URL}/auth/me`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.ok) { setUser(await res.json()); } else { localStorage.removeItem("token"); }
    } catch { localStorage.removeItem("token"); }
    setLoading(false);
  }, []);

  useEffect(() => { checkAuth(); }, [checkAuth]);

  const login = async (email, password) => {
    const res = await fetch(`${BASE_URL}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Login failed");
    }
    const data = await res.json();
    localStorage.setItem("token", data.token);
    setUser(data.user);
  };

  const signup = async (name, email, password) => {
    const res = await fetch(`${BASE_URL}/auth/signup`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, email, password }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.detail || "Signup failed");
    }
    const data = await res.json();
    localStorage.setItem("token", data.token);
    setUser(data.user);
  };

  const logout = () => { localStorage.removeItem("token"); setUser(null); };

  return { user, loading, login, signup, logout };
}

/* ---- Spending bar chart ---- */
function SpendingChart({ data }) {
  if (!data.length) return null;
  const maxVal = data[0][1];
  return (
    <div className="spending-chart">
      {data.map(([cat, val]) => {
        const pct = Math.max((val / maxVal) * 100, 4);
        return (
          <div key={cat} className="chart-row">
            <span className="chart-label">{cap(cat)}</span>
            <div className="chart-bar-wrap">
              <div className="chart-bar" style={{ width: `${pct}%`, background: catColor(cat) }}>
                <span className="chart-bar-val">{fmt(val)}</span>
              </div>
            </div>
          </div>
        );
      })}
      <div className="chart-legend">
        {data.map(([cat]) => (
          <span key={cat} className="legend-item">
            <span className="legend-dot" style={{ background: catColor(cat) }} />
            {cap(cat)}
          </span>
        ))}
      </div>
    </div>
  );
}


function Logo() {
  return (
    <div className="logo">
      <span className="brand">AlloyFinance</span>
    </div>
  );
}

export default function App() {
  const auth = useAuth();

  if (auth.loading) {
    return (
      <div style={{ minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center", color: "#64748b" }}>
        Loading…
      </div>
    );
  }

  return (
    <Routes>
      <Route
        path="/performance"
        element={!auth.user ? <LoginScreen auth={auth} /> : <PerformanceLayout auth={auth} />}
      />
      <Route
        path="*"
        element={!auth.user ? <LoginScreen auth={auth} /> : <Dashboard auth={auth} />}
      />
    </Routes>
  );
}

function PerformanceLayout({ auth }) {
  return (
    <div className="app app--wide">
      <header className="header">
        <div className="header-row">
          <div>
            <h1>AlloyFinance</h1>
            <p className="header-sub">Performance</p>
          </div>
          <div className="user-info">
            {auth.user.picture && (
              <img className="avatar" src={auth.user.picture} alt="" referrerPolicy="no-referrer" />
            )}
            <span className="user-name">{auth.user.name || auth.user.email}</span>
            <Link
              to="/"
              className="btn btn-filter"
              style={{ textDecoration: "none", display: "inline-block" }}
            >
              Back to app
            </Link>
            <button className="btn btn-logout" onClick={auth.logout}>
              Sign out
            </button>
          </div>
        </div>
      </header>
      <main className="main">
        <PerformanceDashboard />
      </main>
    </div>
  );
}

function LoginScreen({ auth }) {
  const [mode, setMode] = useState("login");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (e) => {
    e.preventDefault();
    setError("");
    setSubmitting(true);
    try {
      if (mode === "signup") await auth.signup(name, email, password);
      else await auth.login(email, password);
    } catch (err) {
      setError(err.message || "Authentication failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login-container">
      <div className="login-card">
        <div className="login-brand">AlloyFinance</div>
        <p className="login-sub">
          {mode === "signup" ? "Create your account" : "Log in to your Account"}
        </p>
        {error && <p className="global-error">{error}</p>}
        <form className="auth-form" onSubmit={handleSubmit}>
          {mode === "signup" && (
            <label className="field">
              <span>Name (optional)</span>
              <input value={name} onChange={(e) => setName(e.target.value)} type="text" placeholder="Jane Doe" />
            </label>
          )}
          <label className="field">
            <span>Email</span>
            <input value={email} onChange={(e) => setEmail(e.target.value)} type="email" placeholder="foo@bar.com" required />
          </label>
          <label className="field">
            <span>Password</span>
            <div className="password-input-wrap">
              <input
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                type={showPassword ? "text" : "password"}
                placeholder="••••••••"
                required
                minLength={8}
              />
              <button type="button" className="password-toggle" onClick={() => setShowPassword((p) => !p)}>
                {showPassword ? "Hide" : "Show"}
              </button>
            </div>
          </label>
          <button className="btn btn-primary auth-submit" type="submit" disabled={submitting}>
            {submitting ? "Please wait…" : mode === "signup" ? "Create account" : "Sign in"}
          </button>
        </form>
        <button
          className="auth-toggle"
          type="button"
          onClick={() => { setMode((m) => (m === "login" ? "signup" : "login")); setError(""); }}
        >
          {mode === "signup" ? "Already have an account? Sign in" : "Need an account? Sign up"}
        </button>
      </div>
    </div>
  );
}

function Dashboard({ auth }) {
  const {
    transactions, loading, error, activeFilter,
    fetchRecent, fetchCurrentMonth, fetchPreviousMonth, fetchAll, createTransaction,
  } = useTransactions();

  const [tab, setTab] = useState("transactions");
  const [showForm, setShowForm] = useState(false);
  const [timeFilter, setTimeFilter] = useState("recent");
  const [categoryFilter, setCategoryFilter] = useState("");
  const [categories, setCategories] = useState([]);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState(null);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [searchPage, setSearchPage] = useState(0);

  useEffect(() => {
    transactionsApi.getCategories().then(setCategories).catch(() => {});
  }, []);

  const handleCategoryChange = (e) => {
    setCategoryFilter(e.target.value);
  };

  const applyTimeFilter = useCallback((nextFilter) => {
    setTimeFilter(nextFilter);
    if (nextFilter === "all-time") fetchAll();
    else if (nextFilter === "current-month") fetchCurrentMonth();
    else if (nextFilter === "previous-month") fetchPreviousMonth();
    else fetchRecent();
  }, [fetchAll, fetchCurrentMonth, fetchPreviousMonth, fetchRecent]);

  useEffect(() => {
    // Keep selected time chip in sync with the hook's source data mode.
    if (activeFilter === "all-time") setTimeFilter("all-time");
    else if (activeFilter === "current-month") setTimeFilter("current-month");
    else if (activeFilter === "previous-month") setTimeFilter("previous-month");
    else if (activeFilter === "recent") setTimeFilter("recent");
    else if (activeFilter.startsWith("category:")) setTimeFilter("recent");
  }, [activeFilter]);

  const categoryMatches = useCallback((txnCategory, selectedCategory) => (
    String(txnCategory || "").toLowerCase() === String(selectedCategory || "").toLowerCase()
  ), []);

  const timeFilterLabel = useCallback(() => {
    if (timeFilter === "all-time") return "All time";
    if (timeFilter === "current-month") return "This month";
    if (timeFilter === "previous-month") return "Last month";
    return "Recent transactions";
  }, [timeFilter]);

  const filterLabel = useCallback(() => {
    if (!categoryFilter) return timeFilterLabel();
    return `${timeFilterLabel()} · Category: ${categoryFilter}`;
  }, [categoryFilter, timeFilterLabel]);

  const filteredTransactions = useMemo(() => {
    if (!categoryFilter) return transactions;
    return transactions.filter((t) => categoryMatches(t.spending_category, categoryFilter));
  }, [transactions, categoryFilter, categoryMatches]);

  const stats = useMemo(() => {
    const expenses = filteredTransactions
      .reduce((s, t) => s + parseFloat(t.amount), 0);
    return { expenses };
  }, [filteredTransactions]);

  const categorySpending = useMemo(() => {
    const map = {};
    transactions.forEach((t) => {
      const amt = parseFloat(t.amount);
      if (amt > 0) {
        const cat = t.spending_category || "other";
        map[cat] = (map[cat] || 0) + amt;
      }
    });
    return Object.entries(map).sort((a, b) => b[1] - a[1]).slice(0, 7);
  }, [transactions]);

  const handleCreate = async (txn) => {
    await createTransaction(txn);
    setShowForm(false);
  };

  const handleSearch = async (e) => {
    e?.preventDefault();
    if (!searchQuery.trim()) return;
    setSearchLoading(true);
    setSearchError("");
    setSearchResults(null);
    setSearchPage(0);
    try {
      const results = await searchApi.search(searchQuery.trim());
      setSearchResults(results);
    } catch (err) {
      setSearchError(err.message);
    } finally {
      setSearchLoading(false);
    }
  };

  return (
    <div className="app-shell">
      {/* Navbar */}
      <nav className="navbar">
        <div className="navbar-inner">
          <Logo />
          <div className="nav-links">
            {[
              ["transactions", "Transactions"],
              ["monthly-reports", "Monthly Reports"],
              ["nl2sql", "NL2SQL"],
              ["search", "Semantic Search"],
              ["insights", "Insights"],
              ["semantic-clusters", "Semantic Clusters"],
            ].map(([t, label]) => (
              <button
                key={t}
                className={`nav-link${tab === t ? " active" : ""}`}
                onClick={() => setTab(t)}
              >
                {label}
              </button>
            ))}
          </div>
          <div className="nav-user">
            {auth.user.picture && (
              <img className="avatar" src={auth.user.picture} alt="" referrerPolicy="no-referrer" />
            )}
            <span className="user-name">{auth.user.name || auth.user.email}</span>
            <button className="btn-signout" onClick={auth.logout}>Sign Out</button>
          </div>
        </div>
      </nav>

      {/* Hero */}
      <div className="hero">
        <h1 className="hero-title">My Budget Dashboard</h1>
        <p className="hero-sub">Tracking your income, spending, and savings</p>
        {tab === "transactions" && (
          <button className="btn-hero" onClick={() => setShowForm(true)}>
            + New Transaction
          </button>
        )}
      </div>

      {/* New Transaction Modal */}
      {showForm && (
        <div className="modal-overlay" onClick={() => setShowForm(false)}>
          <div className="modal-card" onClick={(e) => e.stopPropagation()}>
            <button className="modal-close" onClick={() => setShowForm(false)} aria-label="Close">✕</button>
            <TransactionForm onSubmit={handleCreate} categories={categories} />
          </div>
        </div>
      )}

      {/* Page content */}
      <div className="page-wrap">
        {tab === "transactions" && (
          <>
            {/* Spending chart — always shows recent transactions */}
            {categorySpending.length > 0 && (
              <div className="card">
                <div className="table-header">
                  <div>
                    <h2>Recent Spending by Category</h2>
                    <p className="chart-total">{fmt(stats.expenses)} total · {categorySpending.length} categories</p>
                  </div>
                  <span className="stat-value expenses-val">{fmt(stats.expenses)}</span>
                </div>
                <SpendingChart data={categorySpending} />
              </div>
            )}

            {/* Filter controls */}
            <section className="controls">
              <div className="filter-group">
                <button
                  className={`btn btn-filter ${timeFilter === "recent" ? "active" : ""}`}
                  onClick={() => applyTimeFilter("recent")}
                >
                  Recent
                </button>
                <button
                  className={`btn btn-filter ${timeFilter === "all-time" ? "active" : ""}`}
                  onClick={() => applyTimeFilter("all-time")}
                >
                  All Time
                </button>
                <button
                  className={`btn btn-filter ${timeFilter === "current-month" ? "active" : ""}`}
                  onClick={() => applyTimeFilter("current-month")}
                >
                  This Month
                </button>
                <button
                  className={`btn btn-filter ${timeFilter === "previous-month" ? "active" : ""}`}
                  onClick={() => applyTimeFilter("previous-month")}
                >
                  Last Month
                </button>
                <select className="category-select" value={categoryFilter} onChange={handleCategoryChange}>
                  <option value="">All Categories</option>
                  {categories.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </div>
            </section>

            {error && <p className="global-error">{error}</p>}

            <section className="card">
              <div className="table-header">
                <h2>{filterLabel()}</h2>
                <span className="txn-count">{filteredTransactions.length} transactions</span>
              </div>
              <TransactionTable transactions={filteredTransactions} loading={loading} />
            </section>

          </>
        )}

        <main className="main">
          {tab === "nl2sql" && <NL2SQLPanel userId={auth.user?.id} />}
          {tab === "monthly-reports" && <MonthlyReportsPanel />}
          {tab === "insights" && <CustomerPanel />}
          {tab === "semantic-clusters" && <SemanticClusters />}

          {tab === "search" && (
            <section className="card">
              <div className="table-header">
                <h2>Semantic Search</h2>
              </div>
              <form onSubmit={handleSearch} style={{ display: "flex", gap: "0.5rem", marginBottom: "0.75rem" }}>
                <input
                  className="category-select"
                  style={{ flex: 1 }}
                  type="text"
                  placeholder="e.g. kitchen items and gifts"
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") handleSearch(); }}
                />
                <button className="btn btn-primary" type="submit" disabled={searchLoading}>
                  {searchLoading ? "Searching…" : "Search"}
                </button>
              </form>
              {searchError && <p className="global-error">{searchError}</p>}
              {searchResults !== null && (() => {
                const PAGE_SIZE = 10;
                const totalPages = Math.ceil(searchResults.length / PAGE_SIZE);
                const pageSlice = searchResults.slice(searchPage * PAGE_SIZE, (searchPage + 1) * PAGE_SIZE);
                return (
                  <>
                    <div className="table-header" style={{ marginTop: "0.5rem" }}>
                      <span className="txn-count">
                        {searchResults.length === 0
                          ? "No similar transactions found"
                          : `${searchResults.length} similar transaction${searchResults.length !== 1 ? "s" : ""}`}
                      </span>
                      {totalPages > 1 && (
                        <span style={{ fontSize: "0.82rem", color: "#64748b" }}>
                          Page {searchPage + 1} of {totalPages}
                        </span>
                      )}
                    </div>
                    <TransactionTable transactions={pageSlice} loading={false} />
                    {totalPages > 1 && (
                      <div className="pagination-row">
                        <button
                          className="btn btn-filter"
                          onClick={() => setSearchPage((p) => p - 1)}
                          disabled={searchPage === 0}
                        >
                          ← Prev
                        </button>
                        <span className="pagination-info">{searchPage + 1} / {totalPages}</span>
                        <button
                          className="btn btn-filter"
                          onClick={() => setSearchPage((p) => p + 1)}
                          disabled={searchPage >= totalPages - 1}
                        >
                          Next →
                        </button>
                      </div>
                    )}
                  </>
                );
              })()}
            </section>
          )}
        </main>
      </div>
    </div>
  );
}
