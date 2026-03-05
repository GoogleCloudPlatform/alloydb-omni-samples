# pgrr: Postgres Record & Replay

`pgrr` is a command-line tool that captures and replays Postgres wire-protocol traffic.

It runs as a **transparent proxy** between your client and a real Postgres instance, recording every message exchanged. The capture file can then be replayed against **any** Postgres instance — a test database, a staging server, a fresh Docker container, or a different host entirely.

---

## Installation

```bash
cd experimental-tools/pgrr
pip install -e .
```

Verify it works:

```bash
pgrr --help
```

---

## How It Works

```
psql (client)
    │
    │  connects to proxy port (e.g. 5433)
    ▼
pgrr proxy  ──── records all messages to queries.json
    │
    │  forwards traffic transparently
    ▼
Postgres (real DB, e.g. port 5432)
```

After capture, `pgrr replay` reads `queries.json` and re-sends the client-side messages to **any target Postgres** you choose — not necessarily the one that was captured against.

---

## End-to-End: Capture on DB A, Replay on DB B

This is the primary use case: capture traffic from a production or source database, then replay it against a test or destination database.

### Step 1 — Set up your source and target databases

**Source DB** (the one you capture from):
```bash
# Example: local Postgres on port 5432
psql -h 127.0.0.1 -p 5432 -U myuser sourcedb -c "\l"
```

**Target DB** (the one you replay into — must exist with the same user):
```bash
# Example: a fresh DB on a different port (Docker)
docker run --rm -e POSTGRES_PASSWORD=mypassword -p 5434:5432 postgres:16-alpine

# or just a different local database
createdb -h 127.0.0.1 -p 5432 -U myuser targetdb
```

---

### Step 2 — Start the proxy (Terminal 1)

The proxy sits in front of your **source** database and records all traffic.

```bash
pgrr capture --port 5433 --upstream-host 127.0.0.1 --upstream-port 5432
```

The proxy will print:
```
Transparent PG proxy listening on 0.0.0.0:5433
Upstream Postgres: 127.0.0.1:5432
Capture file: queries.json
```

> The proxy runs in the **foreground** and blocks this terminal. Leave it running.

---

### Step 3 — Run your queries through the proxy (Terminal 2)

Point your client at the **proxy port** (5433), not the real database port.

```bash
psql -h 127.0.0.1 -p 5433 -U myuser sourcedb
```

Then run whatever SQL you want to capture:

```sql
CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT, email TEXT);
INSERT INTO users (name, email) VALUES ('Alice', 'alice@example.com');
INSERT INTO users (name, email) VALUES ('Bob', 'bob@example.com');
SELECT * FROM users;
\q
```

All traffic is transparently forwarded to the real DB and recorded to `queries.json`.

#### Alternative: Use `pgbench` to generate load

Instead of writing queries manually, you can use `pgbench` to generate realistic benchmark traffic through the proxy:

```bash
# Initialize the pgbench schema on your source DB first (direct, not through proxy)
pgbench -h 127.0.0.1 -p 5432 -U myuser -i sourcedb

# Then run pgbench through the proxy to capture the traffic
pgbench -h 127.0.0.1 -p 5433 -U myuser -c 1 -t 100 sourcedb
```

- `-i` initializes the benchmark tables (`pgbench_accounts`, `pgbench_tellers`, etc.) — do this **directly** on the source DB, not through the proxy
- `-c 1` uses 1 client connection
- `-t 100` runs 100 transactions

This captures a full realistic workload (SELECT, UPDATE, INSERT across multiple tables) without writing any SQL manually.

> **Tip**: run `pgbench -i` directly on port 5432 (bypassing the proxy) so the schema setup isn't included in your capture. Then run the actual benchmark transactions through port 5433.

---

### Step 4 — Stop the proxy (Terminal 1)

Press `Ctrl+C` in Terminal 1. The proxy will write a summary record and exit:

```
[summary] total_records=18, skipped_records=0
```

---

### Step 5 — (Optional) Patch the capture for the target DB

If your target database has a **different name or user** than the source, patch the capture file so the StartupPacket uses the right credentials:

```bash
pgrr patch-capture queries.json --db-user myuser --db-name targetdb
```

This rewrites the connection parameters in the capture in-place. To write to a new file instead:

```bash
pgrr patch-capture queries.json --db-user myuser --db-name targetdb --output patched.json
```

> Skip this step if the target DB has the same name and user as the source.

---

### Step 6 — Replay into the target DB (Terminal 2)

```bash
# Replay into a different local database
pgrr replay --capture queries.json --host 127.0.0.1 --port 5432 --db-name targetdb

# Replay into a Docker container on port 5434
pgrr replay --capture queries.json --host 127.0.0.1 --port 5434

# Replay at half speed (useful for timing-sensitive workloads)
pgrr replay --capture queries.json --host 127.0.0.1 --port 5434 --speed 0.5

# Replay as fast as possible (ignore captured timing)
pgrr replay --capture queries.json --host 127.0.0.1 --port 5434 --speed 0.0
```

Replay logs every sent message to `replay_log.jsonl`.

---

### Step 7 — Verify

Connect directly to the target and confirm the data landed:

```bash
psql -h 127.0.0.1 -p 5434 -U myuser targetdb -c "SELECT * FROM users;"
```

---

## Quick Reference

| Terminal | Command |
|----------|---------|
| 1 | `pgrr capture --port 5433 --upstream-host <src-host> --upstream-port <src-port>` |
| 2 | `psql -h 127.0.0.1 -p 5433 -U <user> <sourcedb>` — run your queries, then `\q` |
| 1 | `Ctrl+C` to stop and flush the capture |
| 2 | *(optional)* `pgrr patch-capture queries.json --db-user <user> --db-name <targetdb>` |
| 2 | `pgrr replay --capture queries.json --host <target-host> --port <target-port>` |

---

## CLI Reference

### `pgrr capture`

```
pgrr capture [OPTIONS]

  --port INTEGER           Port for the proxy to listen on  [default: 5433]
  --upstream-host TEXT     Upstream Postgres host to forward to  [default: 127.0.0.1]
  --upstream-port INTEGER  Upstream Postgres port  [default: 5432]
  --output TEXT            Capture output file  [default: queries.json]
```

### `pgrr replay`

```
pgrr replay [OPTIONS]

  --capture TEXT   Capture file to replay  [default: queries.json]
  --host TEXT      Target Postgres host  [default: 127.0.0.1]
  --port INTEGER   Target Postgres port  [default: 5432]
  --speed FLOAT    Replay speed multiplier (1.0=real-time, 0.5=half, 0.0=fastest)  [default: 1.0]
```

### `pgrr patch-capture`

```
pgrr patch-capture CAPTURE_FILE [OPTIONS]

  --db-user TEXT   Override the database user in the StartupPacket
  --db-name TEXT   Override the database name in the StartupPacket
  --output TEXT    Write patched capture to this file (default: overwrite in-place)
```

---

## Notes

- **Authentication**: the target database must accept the user from the capture (or patched) StartupPacket. Make sure `pg_hba.conf` allows the connection, or use `trust` auth for local testing.
- **Schema must exist**: replay sends the exact SQL that was captured. If your target DB doesn't have the same schema, DDL statements in the capture will create it — but if you're only replaying DML, create the schema first.
- **SSL**: `pgrr` handles the SSL negotiation transparently. You do not need to set `PGSSLMODE=disable`.
- **Multiple sessions**: if multiple clients connected during capture, each session is replayed concurrently on its own connection.
- **Capture file format**: newline-delimited JSON (one record per line). Each record includes `msg_type`, `sql` (for `Q` messages), `raw_hex`, `direction`, `capture_time`, `db_user`, and `db_name`.




**Simple test** 
---

**pgrr + pgbench End-to-End Walkthrough**

**Prerequisites**
```
cd experimental-tools/pgrr
pip install -e .
```

**1. Clean up any previous runs**
```
kill $(lsof -i :5433 -t) 2>/dev/null
rm -f queries.json replay_log.jsonl
```

**2. Create source and target databases**
```
dropdb -h 127.0.0.1 -p 5432 -U <YOUR USER> sourcedb 2>/dev/null
createdb -h 127.0.0.1 -p 5432 -U <YOUR USER> sourcedb

dropdb -h 127.0.0.1 -p 5432 -U <YOUR USER> targetdb 2>/dev/null
createdb -h 127.0.0.1 -p 5432 -U <YOUR USER> targetdb
```

**3. Initialize pgbench schema on source DB (direct — bypass proxy)**
```
pgbench -h 127.0.0.1 -p 5432 -U <YOUR USER> -i sourcedb
```

**4. Start the proxy — Terminal 1 (leave running)**
```
pgrr capture --port 5433 --upstream-host 127.0.0.1 --upstream-port 5432
```

**5. Run pgbench through the proxy — Terminal 2**
```
pgbench -h 127.0.0.1 -p 5433 -U <YOUR USER> -c 1 -t 10 sourcedb
```

**6. Stop the proxy — Terminal 1**
```
Ctrl+C
```

**7. Initialize pgbench schema on target DB (direct — bypass proxy)**
```
pgbench -h 127.0.0.1 -p 5432 -U <YOUR USER> -i targetdb
```

**8. Patch the capture file to point at targetdb**
```
pgrr patch-capture queries.json --user <YOUR USER> --db targetdb
```

**9. Replay into targetdb — Terminal 2**
```
pgrr replay --capture queries.json --host 127.0.0.1 --port 5432
```

**10. Verify the data landed**
```
psql -h 127.0.0.1 -p 5432 -U <YOUR USER> targetdb -c "SELECT count(*) FROM pgbench_history;"
psql -h 127.0.0.1 -p 5432 -U <YOUR USER> sourcedb  -c "SELECT count(*) FROM pgbench_history;"
```

Both should return `10` (matching the `-t 10` transactions you ran).
