# 🎯 Complete End-to-End Flow: PostgreSQL Capture & Replay

## Overview
This guide walks you through capturing PostgreSQL queries and replaying them step-by-step.

---

## 📦 Prerequisites

1. **PostgreSQL Database Running**
   ```bash
   # Check if PostgreSQL is running
   pg_isready -h localhost -p 5432
   ```

2. **Install Dependencies**
   ```bash
   pip install psycopg2-binary
   ```

3. **Project Structure**
   ```
   pgrr/
   ├── pgrr/
   │   └── proxy.py          # The proxy that captures traffic
   ├── sql_replay.py          # SQL re-execution tool (recommended)
   └── queries.json           # Will be created by proxy
   ```

---

## 🚀 Step-by-Step Walkthrough

### **PHASE 1: Start the Capture Proxy** 

#### Terminal 1 - Start Proxy
```bash
# Navigate to the pgrr/pgrr directory
cd /path/to/pgrr/pgrr

# Start the proxy (default: listen on 5433, forward to localhost:5432)
python3 proxy.py
```

**What you'll see:**
```
======================================================================
PostgreSQL Query Capture Proxy
======================================================================
Listening on:    0.0.0.0:5433
Forwarding to:   127.0.0.1:5432
Capture file:    queries.json
======================================================================

Connect to the proxy using:
  psql -h localhost -p 5433 -U <user> -d <database>

Press Ctrl+C to stop the proxy
======================================================================

Transparent PG proxy listening on 0.0.0.0:5433
Upstream Postgres: 127.0.0.1:5432
Capture file: queries.json
Capture time: per-record (capture_time field)
```

✅ **Proxy is now running and ready to capture!**

---

### **PHASE 2: Run Queries Through the Proxy**

#### Terminal 2 - Connect via Proxy
```bash
# Connect to your database THROUGH THE PROXY (port 5433, not 5432!)
psql -h localhost -p 5433 -U your_user -d your_database
```

**Example queries to run:**
```sql
-- Run some queries you want to capture
SELECT COUNT(*) FROM track;

SELECT * FROM album LIMIT 10;

SELECT name, milliseconds 
FROM track 
WHERE track_id < 20;

SELECT ar.name, COUNT(a.album_id) as album_count
FROM artist ar
LEFT JOIN album a ON ar.artist_id = a.artist_id
GROUP BY ar.name
HAVING COUNT(a.album_id) > 3
ORDER BY album_count DESC;

-- Exit when done
\q
```

**What's happening:**
- 📥 Your queries go to the proxy (port 5433)
- 🔄 Proxy forwards them to PostgreSQL (port 5432)
- 💾 Proxy captures ALL traffic to `queries.json`
- 📤 Results come back to you normally

✅ **Queries are now captured in queries.json!**

---

###  **PHASE 3: Stop the Proxy**

Back in Terminal 1, press `Ctrl+C`:

```
^C
Shutting down proxy...
[summary] total_records=45, skipped_records=0
```

You'll now have a `queries.json` file with all captured traffic!

---

### **PHASE 4: Inspect Captured Sessions**

#### Terminal 3 - List Sessions
```bash
# Navigate to the root pgrr directory
cd /path/to/pgrr

# List all captured client sessions
python3 sql_replay.py --dry-run --dbname chinook --user your_user
```

Or use the helper script:
```bash
python3 smart_replay.py
```

**What you'll see:**
```
Found 2 client session(s):

1. Client port 54737:
   - 15 total packets
   - 0 SQL queries

2. Client port 54752:
   - 45 total packets
   - 10 SQL queries
   - Sample queries:
     • SELECT COUNT(*) FROM track;...
     • SELECT * FROM album LIMIT 10;...
     • SELECT name, milliseconds FROM track WHERE track_id < 20;...
```

📝 **Note the client port with SQL queries** (e.g., 54752)

---

### **PHASE 5: Replay Queries**

Now you can replay the captured queries to ANY database!

#### Option A: SQL Replay (Recommended ⭐)

```bash
# Dry run first (see what will be executed)
python3 sql_replay.py \
  --capture-file pgrr/queries.json \
  --client-port 54752 \
  --dbname chinook \
  --user your_user \
  --dry-run
```

**Output:**
```
Extracting SQL queries from pgrr/queries.json...
Found 10 SQL queries.

[DRY RUN MODE - Not connecting to database]

1. SELECT COUNT(*) FROM track;
2. SELECT * FROM album LIMIT 10;
3. SELECT * FROM artist WHERE name LIKE 'A%' LIMIT 3;
...
10. SELECT ar.name, COUNT(a.album_id) as album_count...
```

**Actually execute:**
```bash
# Execute queries (instant replay, no delays)
python3 sql_replay.py \
  --capture-file pgrr/queries.json \
  --client-port 54752 \
  --dbname chinook \
  --user your_user \
  --speed 0
```

**Output:**
```
Extracting SQL queries from pgrr/queries.json...
Found 10 SQL queries.

Found 10 SQL queries to execute.
Connecting to localhost:5432/chinook as your_user...
Connected.

[1/10] Executing: SELECT COUNT(*) FROM track;
  → 1 rows returned
     (2503,)

[2/10] Executing: SELECT * FROM album LIMIT 10;
  → 10 rows returned
     (1, 'For Those About To Rock We Salute You', 1)
     (2, 'Balls to the Wall', 2)
     (3, 'Restless and Wild', 2)
     ... 7 more rows

...

Done. Executed: 10, Failed: 0
```

✅ **Queries successfully replayed!**

---

## 🎨 Advanced Usage Examples

### Example 1: Capture from Production, Replay to Dev

```bash
# 1. Capture from production
python3 proxy.py \
  --target-host prod-db.company.com \
  --target-port 5432 \
  --capture-file prod_queries.json

# 2. (In another terminal) Connect and run queries
psql -h localhost -p 5433 -U prod_user -d production
# ... run queries ...
\q

# 3. Replay to dev database
python3 sql_replay.py \
  --capture-file pgrr/prod_queries.json \
  --client-port 54752 \
  --host dev-db.company.com \
  --port 5432 \
  --dbname development \
  --user dev_user \
  --speed 0
```

### Example 2: Replay with Original Timing

```bash
# Replay at original speed (good for timing analysis)
python3 sql_replay.py \
  --capture-file pgrr/queries.json \
  --client-port 54752 \
  --dbname chinook \
  --user your_user \
  --speed 1.0
```

### Example 3: Replay at 10x Speed (Stress Test)

```bash
# Replay 10 times faster (for stress testing)
python3 sql_replay.py \
  --capture-file pgrr/queries.json \
  --client-port 54752 \
  --dbname chinook \
  --user your_user \
  --speed 10.0
```

### Example 4: Interactive Smart Replay

```bash
# Use the helper script for guided replay
python3 smart_replay.py
```

It will prompt you for:
- Database name
- User
- Host
- Port
- Speed multiplier

---

## 📊 Visual Flow Diagram

```
┌────────────────────────────────────────────────────────────────┐
│                    CAPTURE PHASE                               │
│                                                                │
│  [Your Client]                                                 │
│       │                                                        │
│       │ psql -h localhost -p 5433                            │
│       ▼                                                        │
│  ┌─────────────────────┐                                      │
│  │   proxy.py          │  Captures traffic                    │
│  │   Port 5433         │────────────────►queries.json         │
│  └─────────────────────┘                                      │
│       │                                                        │
│       │ Forwards to port 5432                                 │
│       ▼                                                        │
│  [PostgreSQL]                                                  │
│  localhost:5432                                                │
└────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────┐
│                    REPLAY PHASE                                │
│                                                                │
│  queries.json                                                  │
│       │                                                        │
│       │ Read queries                                           │
│       ▼                                                        │
│  ┌─────────────────────┐                                      │
│  │  sql_replay.py      │                                      │
│  │  --client-port 54752│                                      │
│  │  --speed 0          │                                      │
│  └─────────────────────┘                                      │
│       │                                                        │
│       │ Execute queries with psycopg2                         │
│       ▼                                                        │
│  [PostgreSQL]                                                  │
│  localhost:5432 (or any other database!)                      │
└────────────────────────────────────────────────────────────────┘
```

---

## 🔍 Troubleshooting

### Issue: "No client sessions found"

**Solution:**
```bash
# Make sure queries.json exists and has content
ls -lh pgrr/queries.json

# Check the file content
head pgrr/queries.json
```

### Issue: "Connection lost" during replay

**Solution:** You're probably using raw `replay.py` instead of `sql_replay.py`. Use `sql_replay.py` for reliable replay.

### Issue: "Database connection failed"

**Solution:**
```bash
# Test database connection first
psql -h target-host -p target-port -U user -d database

# Make sure credentials are correct
```

### Issue: "Wrong queries being replayed"

**Solution:**
```bash
# Always specify --client-port to filter to one session
python3 sql_replay.py --dry-run --dbname mydb --user myuser
# Look at the output and pick the right client port
```

---

## 📝 Quick Reference

### Capture Commands
```bash
# Default
python3 proxy.py

# Custom target
python3 proxy.py --target-host db.example.com --target-port 5432

# Custom listen port
python3 proxy.py --listen-port 6543

# Custom capture file
python3 proxy.py --capture-file my_queries.json
```

### Replay Commands
```bash
# Dry run (see queries without executing)
python3 sql_replay.py --dry-run --dbname mydb --user myuser

# Execute queries
python3 sql_replay.py --client-port 54752 --dbname mydb --user myuser --speed 0

# Replay to different server
python3 sql_replay.py --client-port 54752 --dbname mydb --user myuser --host remote.com --port 5433

# Interactive helper
python3 smart_replay.py
```

---

## ✅ Summary

1. **Start proxy** → `python3 proxy.py`
2. **Run queries** → `psql -h localhost -p 5433 ...`
3. **Stop proxy** → `Ctrl+C`
4. **Replay queries** → `python3 sql_replay.py --client-port <port> --dbname <db> --user <user>`

That's it! You can now capture from any database and replay to any other database! 🎉
