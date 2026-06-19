# alloydb-finance-ai

AI-native personal finance sample demonstrating AlloyDB AI capabilities:
in-database NL→SQL via `alloydb_ai_nl`, pgvector + `text-embedding-005`
semantic search, Gemini-powered explanations, and a tool-router style
MCP integration.

See **[HANDOFF.md](./HANDOFF.md)** for the full architecture, spec→implementation map,
local-development walkthrough (Path A: AlloyDB Cloud via `kubectl port-forward`; Path B: local
Postgres + pgvector), deployment notes, and known gaps.

## Quick start

~~~bash
# 1. port-forward to AlloyDB (Path A) or start local pgvector (Path B) — see HANDOFF.md
# 2. create .env at the repo root (template in HANDOFF.md)
# 3. docker compose up --build
# 4. open http://localhost:5173 and log in as foo@bar.com / password
~~~

This was built as a Codebase x Google club project. The original development repo lives at
https://github.com/diyahasteer/alloyfinance-ai.
