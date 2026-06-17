# AlloyFinance AI — Handoff Notes

**Project:** AlloyDB AI Personal Finance Application (Cloud + Omni)
**Partner:** Google (AlloyDB + AlloyDB Omni)
**Repo:** https://github.com/diyahasteer/alloyfinance-ai
**Purpose of this doc:** Orient a new team (or Google reviewers) to what was built, how it maps to the original spec, how to run/deploy it, and what's left to do.

---

## 1. What this is

A production-style demo of a Mint/Rocket-Money–style personal finance app whose real purpose is to **showcase AlloyDB as an AI-native operational database**. The frontend (React/Vite) is intentionally minimal — it's a vehicle for demonstrating database-backed AI, not a polished consumer fintech UI.

The interesting parts all live in the backend (FastAPI) and in AlloyDB itself:

- **Natural-language → SQL** runs *inside* AlloyDB via `alloydb_ai_nl.get_sql()`, then results are explained by Gemini.
- **Vector / embedding** features (semantic search, clustering) use `pgvector` columns populated by Google's `text-embedding-005` model (both via AlloyDB's in-DB `google_ml.embedding()` and via the Vertex AI REST API).
- **A lightweight "MCP" tool router** uses Gemini to classify a user's chat message and route it to the right tool (NL2SQL, semantic search, monthly reports, clustering, general).
- **Monthly reports** aggregate spending and use Gemini to write a narrative summary + suggestions.
- **Performance instrumentation** times every stage (routing, NL2SQL translation, execution, embedding, vector search, Gemini) and exposes percentile summaries.

---

## 2. Spec → implementation status

The original spec is in `Codebase Google Spec_ AlloyDB AI Personal Finance Application (Cloud + Omni).pdf`. Honest status as of the latest `main`:

| Spec requirement | Status | Notes |
|---|---|---|
| **NL → SQL inside AlloyDB** | ✅ Done | `alloydb_ai_nl.get_sql()` with the `transactions_2_config` config. SQL is generated in-DB, sanitized/scoped to the user, executed, and explained by Gemini. |
| **SQL explanation flow** | ✅ Done | `/api/ai-analysis/nl2sql` runs the query then asks Gemini to explain the results in plain English. Fallback explanation if Gemini fails. |
| **Vector similarity / semantic search** | ✅ Done | `pgvector` `vector(768)` columns; cosine distance (`<=>`) search; merchant/transaction similarity. |
| **Transaction clustering** | ✅ Done | K-means over embeddings, Gemini-generated cluster labels + trend analysis. |
| **Natural-language categorization** | ✅ Done | Semantic filter + category enrichment during ingest. |
| **Agentic AI / MCP integration** | ⚠️ Partial | Single-turn Gemini **tool router** exists (intent → tool). No multi-turn agent loop, no schema-inspection tool, no agent write-back. |
| **Flagged-transaction investigation workflow** | ❌ Not done | No flagging, anomaly detection, risk score, or investigation-summary write-back. **Biggest spec gap.** |
| **Analytical workloads / columnar engine** | ⚠️ Partial | Monthly aggregations exist (on-demand). No columnar-engine configuration and no demonstration of HTAP acceleration. |
| **Auth (basic)** | ✅ Done | JWT + email/password (bcrypt). `google_id` column exists but Google OAuth is not wired on the backend. |
| **Synthetic dataset** | ✅ Done | Deterministic generator under `backend/synthetic-data/`, plus a Kaggle bulk-ingest path. |
| **Dashboard** | ✅ Done | Balances, spending breakdown, budgets, monthly reports, NL2SQL panel, clusters, performance dashboard. |

**TL;DR for the next team:** core AI-in-the-database story (NL2SQL, embeddings, semantic search, clustering, reports) is solid and demoable. The **anomaly/agent-investigation write-back loop** and **pg_cron/columnar analytics**are the major unfinished spec items.

---

## 3. Architecture

```
                React/Vite SPA (frontend/)
                        │  HTTP (VITE_API_URL)
                        ▼
                FastAPI backend (backend/app/main.py)
        ┌───────────────┼───────────────────────────────┐
        │               │                               │
   JWT auth       Vertex AI (Gemini + text-embedding-005)   MCP tool router
        │               │  (ADC creds)                  (backend/mcp/)
        ▼               ▼                               │
                  AlloyDB (PostgreSQL-compatible)  ◀─────┘
                  • alloydb_ai_nl.get_sql()  (NL2SQL)
                  • google_ml.embedding()    (in-DB embeddings)
                  • pgvector vector(768)      (semantic search/clustering)
```

- **Backend:** FastAPI + `asyncpg` connection pool. Single process; no background workers.
- **AI auth:** Application Default Credentials (ADC) — `gcloud auth application-default login` on the host, mounted into the container. The project migrated **off** AI Studio API keys onto Vertex AI; see `VERTEX_MIGRATION.md`.
- **Embeddings:** `text-embedding-005`, **768 dims**. Two paths — on-demand via Vertex REST (`_embed_text()`), and bulk/in-DB via `google_ml.embedding(...)::vector`.
- **NL2SQL:** generated by AlloyDB's managed `alloydb_ai_nl` feature, which requires a DB-side config named `transactions_2_config`.

---

## 4. Repository layout

```
backend/
  app/
    main.py                 # ALL endpoints, auth, NL2SQL, clustering, reports, budgets (~2.3k lines)
    ai_analysis.py          # Gemini calls: insight + monthly-report narration (+ fallbacks)
    monitoring.py           # MetricsStore: in-memory rolling buffer + CSV persistence
    backfill_embeddings.py  # batch embed via in-DB google_ml.embedding()
    test_ai_analysis.py     # unit tests for the Gemini helpers
    test_main_helpers.py    # unit tests for SQL scoping/sanitization helpers
  mcp/
    tool_router.py          # Gemini intent classifier → one of 5 tools
    gemini_json.py          # Vertex AI REST wrapper (JSON-constrained generation)
    constants.py            # tool IDs + enums
  synthetic-data/           # deterministic transaction generator + prompts/config
  embed_transactions_2.py   # real-time embedding script (Vertex REST, retry/backoff)
  ingest_kaggle.py          # bulk load + dedupe + enrich + embed Kaggle retail data
  ingest_transactions.py    # CSV seed loader (currently disabled in startup path)
  requirements.txt, Dockerfile
frontend/
  src/
    App.jsx, components/    # dashboard, NL2SQL panel, clusters, monthly reports, perf dashboard
    api/                    # one module per backend feature
    hooks/                  # useTransactions, useItems
  Dockerfile, nginx.conf, vite.config.js
k8s/                        # namespace, backend/frontend deployments, embed job
scripts/benchmark.py        # load test + percentile report
docker-compose.yml          # local dev (backend + frontend)
DEPLOY.md                   # GKE + AlloyDB Cloud deployment walkthrough
VERTEX_MIGRATION.md         # how to move from AI Studio API key → Vertex AI / ADC
CURL-COMMANDS.md            # quick API reference
transactions_backup.sql     # SQL dump (data backup)
```

---

## 5. Database schema (inferred from `main.py` startup DDL)

Extensions: `vector` (pgvector). NL2SQL also depends on AlloyDB's `alloydb_ai_nl` and a DB-side config `transactions_2_config`.

- **`users`** — `id`, `google_id` (nullable/unused), `email` (unique), `password_hash`, `name`, `picture`, `created_at`.
- **`transactions_2`** *(active table)* — `transaction_id` (uuid), `user_id`, `timestamp`, `amount`, `merchant_name`, `spending_category`, `item_description`, `quantity`, `country`, `embedding vector(768)`.
- **`transactions`** *(legacy, largely superseded by `transactions_2`)* — similar columns + `merchant_category`, `payment_method`, `city`, `currency`, `description`, `embedding`.
- **`BudgetPrefs`** — `category` (PK), `amount`.
- **`monthly_reports`** — PK `(user_id, year_month)`; `total_spent`, `comments`, `suggestions_json`, `category_breakdown_json`, `merchant_breakdown_json`, `generated_at` (+ legacy `summary/highlights/suggestions/totals`).
- **`items`** — generic CRUD demo table, not part of core features.

**Spending categories are hard-coded** (`SpendingCategory` enum in `main.py`): shopping, gifts, household, office, clothing, hobbies, food, beauty, toys, stationery. The same list is mirrored in `ingest_kaggle.py` and `mcp/tool_router.py` — keep them in sync.

---

## 6. Key request flows

- **NL2SQL + explanation** — `POST /api/ai-analysis/nl2sql`: prompt → `alloydb_ai_nl.get_sql()` (uses `transactions_2_config`) → SQL is normalized + scoped to `user_id` and capped (`ROW_LIMIT=200`, `amount < 10000`), SELECT-only → executed → Gemini explains results (fallback if Gemini fails).
- **Semantic search** — `POST /api/transactions/search`: embed query → cosine distance over `embedding`, threshold ~0.55, but **guarantees at least ~5 results** even below threshold.
- **Clustering** — `GET /api/transactions/clusters`: K-means (k≈3–10) over embeddings → Gemini labels + summary bullets + trend vs prior period.
- **Monthly report** — `POST /api/reports/monthly/generate`: aggregate by category/merchant for a month → Gemini narrative → upsert into `monthly_reports`.
- **MCP router** — `POST /api/customer/ask`: Gemini classifies intent → dispatches to NL2SQL / semantic search / monthly reports / general. **Clustering via the router is a stub** (`not_implemented`) — the dedicated `/api/transactions/clusters` endpoint works and the UI uses it directly.
- **Auth** — `POST /auth/signup`, `POST /auth/login` (→ JWT, HS256, 24h), `GET /auth/me`. Dev user `foo@bar.com` / `password` auto-seeded on startup.

API quick reference: `CURL-COMMANDS.md`.

---

## 7. Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | ✅ | — | AlloyDB/Postgres connection string |
| `JWT_SECRET` | ✅ | — | HS256 signing key |
| `GOOGLE_CLOUD_PROJECT` | for AI | — | GCP project for Vertex AI (Gemini + embeddings) |
| `GOOGLE_CLOUD_LOCATION` | — | `us-central1` | Vertex region |
| `GEMINI_MODEL` | — | `gemini-2.0-flash` | Generation model (docs/`.env` often set `gemini-2.5-flash`) |
| `GOOGLE_APPLICATION_CREDENTIALS` | — | — | ADC path; set in compose to the mounted gcloud creds |
| `DATABASE_SSL_MODE` | — | `disable` | `require`/`disable` |
| `CORS_ORIGINS` | — | localhost:5173/4173 | comma-separated allowed origins |
| `METRICS_PERSIST_PATH` | — | `backend/app/data/performance_metrics.csv` | metrics CSV path |
| `GEMINI_API_KEY` | — | — | Legacy AI-Studio key; **unused** on the Vertex path |
| `VITE_API_URL`, `VITE_GOOGLE_CLIENT_ID` | frontend build | — | baked into the frontend image at build time |

You need a `.env`  **and** host ADC creds (`gcloud auth application-default login`).

---

## 8. Local development

The repo's `README.md` is incomplete — it doesn't tell you about `.env`, doesn't explain that `docker compose up` doesn't start a database, and doesn't cover ADC setup. Use the steps below instead.

### Prerequisites (one-time)

- **Docker Desktop** installed and **running** before you `docker compose up` (otherwise you get *"cannot connect to Docker daemon"*).
- **gcloud CLI** authenticated:
  ```bash
  gcloud auth login
  gcloud auth application-default login          # writes ADC creds that the backend container mounts
  gcloud config set project <PROJECT_ID>          # the GCP project with Vertex AI enabled
  ```
- **`kubectl`** authenticated to the GKE cluster (only needed for Path A):
  ```bash
  gcloud container clusters get-credentials alloydb-demo --zone=us-central1-a
  ```

Pick one of the two paths below for the database.

---

### Path A — Use the team's AlloyDB Cloud (full demo, recommended)

This is what the deployed version uses. NL2SQL works because the `transactions_2_config` is already configured inside the live AlloyDB instance.

1. **Make sure the cluster is up.** If the team scaled it to 0 to save money:
   ```bash
   gcloud container clusters resize alloydb-demo \
     --node-pool=default-pool --num-nodes=2 \
     --zone=us-central1-a
   kubectl get pods -n alloydb --watch       # wait for Running
   ```

2. **Port-forward to AlloyDB** in its own terminal (leave it running):
   ```bash
   kubectl port-forward svc/al-my-cluster-rw-ilb 5433:5432 -n alloydb
   ```

3. **Create `.env` at the repo root** (not inside `backend/`):
   ```bash
   DATABASE_URL=postgresql://alloydbadmin:<password>@host.docker.internal:5433/postgres
   JWT_SECRET=any-long-random-string-32-chars-min
   GOOGLE_CLOUD_PROJECT=<PROJECT_ID>
   GOOGLE_CLOUD_LOCATION=us-central1
   GEMINI_MODEL=gemini-2.5-flash
   VITE_API_URL=http://localhost:8000
   VITE_GOOGLE_CLIENT_ID=
   ```
   - The AlloyDB admin password is in `k8s/secrets.yaml` (decode the `DATABASE_URL` value).
   - **`host.docker.internal`** lets the backend container reach the port-forward running on your host. If you run uvicorn natively instead of in Docker, use `127.0.0.1` instead.

4. **Start the app:**
   ```bash
   docker compose up --build
   ```
   Backend → http://localhost:8000, frontend → http://localhost:5173.

5. **Log in** at http://localhost:5173 with the auto-seeded dev user:
   - **email:** `foo@bar.com`
   - **password:** `password`

When you're done, scale the cluster back down to save money:
```bash
gcloud container clusters resize alloydb-demo --node-pool=default-pool --num-nodes=0 --zone=us-central1-a
```

---

### Path B — Local Postgres + pgvector (no GKE access needed)

Good for verifying the code runs. NL2SQL won't work (it depends on AlloyDB's `alloydb_ai_nl` extension), but everything else does once embeddings are populated.

1. **Run pgvector locally:**
   ```bash
   docker run -d --name pgvector \
     -e POSTGRES_PASSWORD=password \
     -p 5432:5432 \
     pgvector/pgvector:pg16
   ```

2. **Create `.env` at the repo root:**
   ```bash
   DATABASE_URL=postgresql://postgres:password@host.docker.internal:5432/postgres
   JWT_SECRET=any-long-random-string-32-chars-min
   GOOGLE_CLOUD_PROJECT=<PROJECT_ID>
   GOOGLE_CLOUD_LOCATION=us-central1
   GEMINI_MODEL=gemini-2.5-flash
   VITE_API_URL=http://localhost:8000
   VITE_GOOGLE_CLIENT_ID=
   ```

3. **Start the app:**
   ```bash
   docker compose up --build
   ```
   The backend's startup hook auto-creates all tables and the `vector` extension on first connect — no migrations to run.

4. **Log in** as `foo@bar.com` / `password`.

5. **Populate embeddings** (optional, but semantic search and clustering will be empty without it):
   ```bash
   docker compose exec backend python embed_transactions_2.py
   ```
   Requires `GOOGLE_CLOUD_PROJECT` and ADC creds.

---

### Native (no Docker) — alternative to either path

```bash
# backend
cd backend && python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
uvicorn app.main:app --reload --port 8000

# frontend (separate terminal)
cd frontend && npm install && npm run dev
```

Use `127.0.0.1` instead of `host.docker.internal` in `DATABASE_URL` when running natively.

---

### Smoke test

```bash
# backend up?
curl http://localhost:8000/api/hello

# login works?
curl -X POST http://localhost:8000/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"foo@bar.com","password":"password"}'
# → should return {"token":"eyJ..."}
```

Open http://localhost:5173 and log in. If you're on Path A and NL2SQL still 500s, the AlloyDB instance you're pointing at is missing the `transactions_2_config` configuration — it was created out-of-band on the live cluster.

---

## 9. Deployment (GKE + AlloyDB Cloud)

Full walkthrough in `DEPLOY.md`. Summary:

- GCP project `alloydbbd`, GKE cluster `alloydb-demo` (`us-central1-a`), namespace `app`.
- Images: `gcr.io/alloydbbd/backend:latest`, `gcr.io/alloydbbd/frontend:latest`.
- Secrets (`k8s/secrets.yaml`): DB URL, Google client ID, JWT secret, CORS origins (base64).
- **CI/CD:** every push to `main` (`.github/workflows/deploy.yml`) builds + pushes both images and `kubectl rollout restart`s both deployments. Requires GitHub secrets `GCP_SA_KEY`, `BACKEND_EXTERNAL_IP`, `VITE_GOOGLE_CLIENT_ID`.
- **Cost control:** scale the node pool to 0 when idle (`gcloud container clusters resize ... --num-nodes=0`); data persists.

> ⚠️ The frontend's `VITE_API_URL` is baked in **at image build time**, so any backend-IP change requires a frontend rebuild.

---

## 10. Known gaps, rough edges & next steps

**Spec items still open (priority order):**
1. **Flagged-transaction investigation workflow** — the whole "flag → agent investigates → similarity search → risk score → write summary back to AlloyDB → user resolves" loop is missing. Needs a `flagged_transactions`/investigation table, anomaly logic, and agent write-back. This is the headline gap.
2. **Columnar engine / pg_cron analytics** — demonstrate HTAP acceleration on heavy rollups and schedule them in-DB via `pg_cron`.
