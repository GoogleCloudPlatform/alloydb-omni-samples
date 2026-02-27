# Complete PostgreSQL Capture & Replay Flow

## Overview

The pgrr toolkit now supports **fully configurable** capture and replay to/from **any PostgreSQL database** on **any host and port**.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    CAPTURE PHASE                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Client (psql)                                                   │
│       │                                                          │
│       │ Connect to localhost:5433                               │
│       ▼                                                          │
│  ┌──────────────────────────────────┐                          │
│  │  proxy.py                         │                          │
│  │  --listen-port 5433               │                          │
│  │  --target-host db.example.com     │──────────────────┐      │
│  │  --target-port 5432               │                  │      │
│  │  --capture-file queries.json      │                  │      │
│  └──────────────────────────────────┘                  │      │
│       │                                                  │      │
│       │ Captures all traffic to queries.json           │      │
│       │                                                  │      │
│       └──────────────────────────────────────────────── ▼      │
│                                                  Source Database │
│                                                  db.example.com  │
│                                                  port 5432       │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                    REPLAY PHASE                                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  queries.json                                                    │
│       │                                                          │
│       │ Read captured queries                                   │
│       ▼                                                          │
│  ┌──────────────────────────────────┐                          │
│  │  sql_replay.py                    │                          │
│  │  --capture-file queries.json      │                          │
│  │  --client-port 54752              │──────────────────┐      │
│  │  --host target-db.local           │                  │      │
│  │  --port 5433                      │                  │      │
│  │  --dbname test_db                 │                  │      │
│  │  --user test_user                 │                  │      │
│  │  --speed 2.0                      │                  │      │
│  └──────────────────────────────────┘                  │      │
│                                                          │      │
│                                                          ▼      │
│                                                  Target Database │
│                                                  target-db.local │
│                                                  port 5433       │
└─────────────────────────────────────────────────────────────────┘
```

## Complete Example Workflows

### Example 1: Capture from Production, Replay to Dev

#### Step 1: Capture Production Traffic
```bash
# Terminal 1: Start proxy pointing to production DB
python3 proxy.py \
  --listen-port 5433 \
  --target-host prod-db.company.com \
  --target-port 5432 \
  --capture-file prod_queries.json

# Terminal 2: Connect through proxy and run queries
psql -h localhost -p 5433 -U prod_user -d production
# Run your queries...
\q
```

#### Step 2: Replay to Development Database
```bash
# Replay to dev database on different server
python3 sql_replay.py \
  --capture-file pgrr/prod_queries.json \
  --client-port 54752 \
  --host dev-db.company.com \
  --port 5432 \
  --dbname development \
  --user dev_user \
  --speed 0
```

### Example 2: Benchmark Different Database Servers

#### Capture Once
```bash
# Capture queries from any database
python3 proxy.py --target-host source-db.com --target-port 5432
psql -h localhost -p 5433 -U user -d mydb
# Run benchmark queries...
```

#### Replay to Multiple Targets
```bash
# Test against Server A
python3 sql_replay.py \
  --dbname mydb --user user \
  --host server-a.com --port 5432 \
  --speed 1

# Test against Server B
python3 sql_replay.py \
  --dbname mydb --user user \
  --host server-b.com --port 5432 \
  --speed 1

# Test against Server C (different port)
python3 sql_replay.py \
  --dbname mydb --user user \
  --host server-c.com --port 5433 \
  --speed 1
```

### Example 3: Cross-Cloud Testing

#### Capture from AWS RDS
```bash
python3 proxy.py \
  --target-host mydb.abc123.us-east-1.rds.amazonaws.com \
  --target-port 5432 \
  --capture-file aws_queries.json
```

#### Replay to Google Cloud SQL
```bash
python3 sql_replay.py \
  --capture-file pgrr/aws_queries.json \
  --host 10.0.0.5 \
  --port 5432 \
  --dbname mydb \
  --user postgres \
  --speed 2
```

## Full Command Reference

### Capture (proxy.py)

```bash
python3 proxy.py [OPTIONS]

Options:
  --listen-port PORT        Port for proxy to listen on (default: 5433)
  --target-host HOST        PostgreSQL host to forward to (default: 127.0.0.1)
  --target-port PORT        PostgreSQL port to forward to (default: 5432)
  --capture-file FILE       File to save traffic (default: queries.json)
```

**Examples:**
```bash
# Local database
python3 proxy.py

# Remote database
python3 proxy.py --target-host db.example.com --target-port 5432

# Custom listen port
python3 proxy.py --listen-port 6543

# Everything custom
python3 proxy.py \
  --listen-port 6543 \
  --target-host 192.168.1.100 \
  --target-port 5433 \
  --capture-file custom_queries.json
```

### Replay (sql_replay.py)

```bash
python3 sql_replay.py [OPTIONS]

Required Options:
  --dbname DATABASE         Database name to connect to
  --user USERNAME           Database user

Optional:
  --client-port PORT        Filter to specific client session (recommended)
  --capture-file FILE       Capture file to read (default: queries.json)
  --host HOST               Database host (default: localhost)
  --port PORT               Database port (default: 5432)
  --password PASS           Database password
  --speed MULTIPLIER        Speed multiplier (default: 1.0, 0 = instant)
  --dry-run                 Show queries without executing
```

**Examples:**
```bash
# Dry run (see what will be executed)
python3 sql_replay.py --dry-run --dbname mydb --user myuser

# Local replay
python3 sql_replay.py \
  --client-port 54752 \
  --dbname mydb \
  --user myuser

# Remote replay
python3 sql_replay.py \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --host remote-db.com \
  --port 5432

# Instant replay (no delays)
python3 sql_replay.py \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --speed 0

# 10x faster
python3 sql_replay.py \
  --client-port 54752 \
  --dbname mydb \
  --user myuser \
  --speed 10
```

### Helper (smart_replay.py)

Interactive wrapper that guides you through replay:

```bash
python3 smart_replay.py
```

This will:
1. Detect available sessions in queries.json
2. Show sample queries from each session
3. Prompt you for database connection details
4. Execute sql_replay.py with your settings

## Key Features

✅ **Flexible Capture**
- Capture from any PostgreSQL host/port
- Custom listen port
- Custom capture file name

✅ **Flexible Replay**
- Replay to any PostgreSQL host/port
- Different database than original
- Different user credentials
- Speed control (0 = instant, 1 = original timing, 10 = 10x faster)

✅ **Use Cases**
- **Migration testing** - Capture from old DB, replay to new DB
- **Performance benchmarking** - Test same queries on different servers
- **Cross-environment testing** - Prod queries on staging/dev
- **Cross-cloud testing** - AWS → GCP, Azure → on-prem, etc.
- **Schema validation** - Ensure queries work on new schema

## Important Notes

### Session Filtering

Always use `--client-port` to filter to a single session:

```bash
# 1. List available sessions
python3 replay.py --list

# 2. Pick a session and replay it
python3 sql_replay.py --client-port <PORT> --dbname mydb --user myuser
```

### Security Considerations

- Captured files contain SQL queries (may include sensitive data)
- Use `--password` argument or let the tool prompt securely
- Keep capture files secure
- Consider using read-only users for replay testing

### Schema Requirements

The target database should have:
- Same schema as source (or compatible schema)
- Necessary permissions for the replay user
- Required extensions/functions installed

## Troubleshooting

### Proxy won't connect to target
```bash
# Test target connection first
psql -h target-host -p target-port -U user -d database
```

### Replay fails on specific queries
```bash
# Use dry-run to see all queries first
python3 sql_replay.py --dry-run --dbname mydb --user myuser

# Check for schema differences between source and target
```

### Wrong session being replayed
```bash
# Always list sessions first
python3 replay.py --list

# Use the correct client port
python3 sql_replay.py --client-port <CORRECT_PORT> ...
```

## Summary

The pgrr toolkit now provides **complete flexibility** for PostgreSQL query capture and replay:

- 📥 **Capture** from **any** PostgreSQL database (local or remote)
- 📤 **Replay** to **any** PostgreSQL database (same or different)
- ⚡ **Control** replay speed (instant, original timing, or custom multiplier)
- 🎯 **Filter** by client session for clean replay
- 🔒 **Secure** password handling
- 📊 **View** results in real-time

Perfect for migration testing, performance benchmarking, cross-environment validation, and more!
